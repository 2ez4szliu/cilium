// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package ipcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"sync"

	cmtypes "github.com/cilium/cilium/pkg/clustermesh/types"
	"github.com/cilium/cilium/pkg/container/bitlpm"
	"github.com/cilium/cilium/pkg/controller"
	"github.com/cilium/cilium/pkg/counter"
	"github.com/cilium/cilium/pkg/identity"
	"github.com/cilium/cilium/pkg/ipcache/types"
	"github.com/cilium/cilium/pkg/labels"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/option"
	"github.com/cilium/cilium/pkg/source"
	"github.com/cilium/cilium/pkg/time"
)

var (
	// ErrLocalIdentityAllocatorUninitialized is an error that's returned when
	// the local identity allocator is uninitialized.
	ErrLocalIdentityAllocatorUninitialized = errors.New("local identity allocator uninitialized")

	LabelInjectorName = "ipcache-inject-labels"

	injectLabelsControllerGroup = controller.NewGroup("ipcache-inject-labels")
)

// clusterID is the type of the key to use in the metadata CIDRTrieMap
type clusterID uint32

// metadata contains the ipcache metadata. Mainily it holds a map which maps IP
// prefixes (x.x.x.x/32) to a set of information (prefixInfo).
//
// When allocating an identity to associate with each prefix, the
// identity allocation routines will merge this set of labels into the
// complete set of labels used for that local (CIDR) identity,
// thereby associating these labels with each prefix that is 'covered'
// by this prefix. Subsequently these labels may be matched by network
// policy and propagated in monitor output.
//
// ```mermaid
// flowchart
//
//	subgraph resourceInfo
//	labels.Labels
//	source.Source
//	end
//	subgraph prefixInfo
//	UA[ResourceID]-->LA[resourceInfo]
//	UB[ResourceID]-->LB[resourceInfo]
//	...
//	end
//	subgraph identityMetadata
//	IP_Prefix-->prefixInfo
//	end
//
// ```
type metadata struct {
	logger *slog.Logger
	// Protects the m map.
	//
	// If this mutex will be held at the same time as the IPCache mutex,
	// this mutex must be taken first and then take the IPCache mutex in
	// order to prevent deadlocks.
	lock.Mutex

	// m is the actual map containing the mappings.
	m map[cmtypes.PrefixCluster]*prefixInfo

	// prefixes is a map of tries. Each trie holds the prefixes for the same
	// clusterID, in order to find descendants efficiently.
	prefixes *bitlpm.CIDRTrieMap[clusterID, struct{}]

	// prefixRefCounter keeps a reference count of all prefixes that come from
	// policy resources, as an optimization, in order to avoid redundantly
	// storing all prefixes from policies.
	prefixRefCounter counter.Counter[cmtypes.PrefixCluster]

	// queued* handle updates into the IPCache. Whenever a label is added
	// or removed from a specific IP prefix, that prefix is added into
	// 'queuedPrefixes'. Each time label injection is triggered, it will
	// process the metadata changes for these prefixes and potentially
	// generate updates into the ipcache, policy engine and datapath.
	queuedChangesMU lock.Mutex
	queuedPrefixes  map[cmtypes.PrefixCluster]struct{}

	// queuedRevision is the "version" of the prefix queue. It is incremented
	// on every *dequeue*. If injection is successful, then injectedRevision
	// is updated and an update broadcast to waiters.
	queuedRevision uint64

	// injectedRevision indicates the current "version" of the queue that has
	// been applied to the ipcache. It is optionally used by ipcache clients
	// to wait for a specific update to be processed. It is protected by a
	// Cond's mutex. When label injection is successful, this will be updated
	// to whatever revision was dequeued and any waiters will be "awoken" via
	// the Cond's Broadcast().
	injectedRevision     uint64
	injectedRevisionCond *sync.Cond

	// reservedHostLock protects the localHostLabels map. Holders must
	// always take the metadata read lock first.
	reservedHostLock lock.Mutex

	// reservedHostLabels collects all labels that apply to the host identity.
	// see updateLocalHostLabels() for more info.
	reservedHostLabels map[netip.Prefix]labels.Labels
}

func newMetadata(logger *slog.Logger) *metadata {
	return &metadata{
		logger:           logger,
		m:                make(map[cmtypes.PrefixCluster]*prefixInfo),
		prefixes:         bitlpm.NewCIDRTrieMap[clusterID, struct{}](),
		prefixRefCounter: make(counter.Counter[cmtypes.PrefixCluster]),
		queuedPrefixes:   make(map[cmtypes.PrefixCluster]struct{}),
		queuedRevision:   1,

		injectedRevisionCond: sync.NewCond(&lock.Mutex{}),

		reservedHostLabels: make(map[netip.Prefix]labels.Labels),
	}
}

// dequeuePrefixUpdates returns the set of queued prefixes, as well as the revision
// that should be passed to setInjectedRevision once label injection has successfully
// completed.
func (m *metadata) dequeuePrefixUpdates() (modifiedPrefixes []cmtypes.PrefixCluster, revision uint64) {
	m.queuedChangesMU.Lock()
	modifiedPrefixes = make([]cmtypes.PrefixCluster, 0, len(m.queuedPrefixes))
	for p := range m.queuedPrefixes {
		modifiedPrefixes = append(modifiedPrefixes, p)
	}
	m.queuedPrefixes = make(map[cmtypes.PrefixCluster]struct{})
	revision = m.queuedRevision
	m.queuedRevision++ // Increment, as any newly-queued prefixes are now subject to the next revision cycle
	m.queuedChangesMU.Unlock()

	return
}

// enqueuePrefixUpdates queues prefixes for label injection. It returns the "next"
// queue revision number, which can be passed to waitForRevision.
func (m *metadata) enqueuePrefixUpdates(prefixes ...cmtypes.PrefixCluster) uint64 {
	m.queuedChangesMU.Lock()
	defer m.queuedChangesMU.Unlock()

	for _, prefix := range prefixes {
		m.queuedPrefixes[prefix] = struct{}{}
	}
	return m.queuedRevision
}

// setInjectectRevision updates the injected revision to a new value and
// wakes all waiters.
func (m *metadata) setInjectedRevision(rev uint64) {
	m.injectedRevisionCond.L.Lock()
	m.injectedRevision = rev
	m.injectedRevisionCond.Broadcast()
	m.injectedRevisionCond.L.Unlock()
}

// waitForRevision waits for the injected revision to be at or above the
// supplied revision. We may skip revisions, as the desired revision is bumped
// every time prefixes are dequeued, but injection may fail. Thus, any revision
// greater or equal to the desired revision is acceptable.
func (m *metadata) waitForRevision(ctx context.Context, rev uint64) error {
	// Allow callers to bail out by cancelling the context
	cleanupCancellation := context.AfterFunc(ctx, func() {
		// We need to acquire injectedRevisionCond.L here to be sure that the
		// Broadcast won't occur before the call to Wait, which would result
		// in a missed signal.
		m.injectedRevisionCond.L.Lock()
		defer m.injectedRevisionCond.L.Unlock()
		m.injectedRevisionCond.Broadcast()
	})
	defer cleanupCancellation()

	m.injectedRevisionCond.L.Lock()
	defer m.injectedRevisionCond.L.Unlock()
	for m.injectedRevision < rev {
		m.injectedRevisionCond.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return nil
}

// canonicalPrefix returns the prefixCluster with its prefix in canonicalized form.
// The canonical version of the prefix must be used for lookups in the metadata prefixCluster
// map. The canonical representation of a prefix has the lower bits of the address always
// zeroed out and does not contain any IPv4-mapped IPv6 address.
func canonicalPrefix(prefixCluster cmtypes.PrefixCluster) cmtypes.PrefixCluster {
	if !prefixCluster.AsPrefix().IsValid() {
		return prefixCluster // no canonical version of invalid prefix
	}

	prefix := prefixCluster.AsPrefix()
	clusterID := prefixCluster.ClusterID()

	// Prefix() always zeroes out the lower bits
	p, err := prefix.Addr().Unmap().Prefix(prefix.Bits())
	if err != nil {
		return prefixCluster // no canonical version of invalid prefix
	}

	return cmtypes.NewPrefixCluster(p, clusterID)
}

// upsertLocked inserts / updates the set of metadata associated with this resource for this prefix.
// It returns the set of affected prefixes. It may return nil if the metadata change is a no-op.
func (m *metadata) upsertLocked(prefix cmtypes.PrefixCluster, src source.Source, resource types.ResourceID, info ...IPMetadata) []cmtypes.PrefixCluster {
	prefix = canonicalPrefix(prefix)
	changed := false

	if _, ok := m.m[prefix]; !ok {
		changed = true
		m.m[prefix] = newPrefixInfo()
		m.prefixes.Upsert(clusterID(prefix.ClusterID()), prefix.AsPrefix(), struct{}{})
	}
	if _, ok := m.m[prefix].byResource[resource]; !ok {
		changed = true
		m.m[prefix].byResource[resource] = &resourceInfo{
			source: src,
		}
	}

	for _, i := range info {
		c := m.m[prefix].byResource[resource].merge(m.logger, i, src)
		changed = changed || c
	}

	// If the metadata for this resource hasn't changed, *or* it has
	// no effect on the flattened metadata, then return zero affected prefixes.
	if !changed || m.m[prefix].flattened.has(src, info) {
		return nil
	}

	// Invalidated flattened metadata. Will be re-populated on next read.
	m.m[prefix].flattened = nil

	return m.findAffectedChildPrefixes(prefix)
}

// GetMetadataSourceByPrefix returns the highest precedence source which has
// provided metadata for this prefix
func (ipc *IPCache) GetMetadataSourceByPrefix(prefix cmtypes.PrefixCluster) source.Source {
	ipc.metadata.Lock()
	defer ipc.metadata.Unlock()
	return ipc.metadata.getLocked(prefix).Source()
}

// get returns a deep copy of the flattened prefix info
func (m *metadata) get(prefix cmtypes.PrefixCluster) *resourceInfo {
	m.Lock()
	defer m.Unlock()
	return m.getLocked(prefix)
}

// getLocked returns a deep copy of the flattened prefix info
func (m *metadata) getLocked(prefix cmtypes.PrefixCluster) *resourceInfo {
	if pi, ok := m.m[canonicalPrefix(prefix)]; ok {
		if pi.flattened == nil {
			// re-compute the flattened set of prefixes
			pi.flattened = pi.flatten(m.logger.With(
				logfields.CIDR, prefix,
				logfields.ClusterID, prefix.ClusterID(),
			))
		}
		return pi.flattened.DeepCopy()
	}
	return nil
}

// mergeParentLabels pulls down all labels from parent prefixes, with "longer" prefixes having
// preference.
//
// Thus, if the ipcache contains:
// - 10.0.0.0/8 -> "a=b, foo=bar"
// - 10.1.0.0/16 -> "a=c"
// - 10.1.1.0/24 -> "d=e"
// the complete set of labels for 10.1.1.0/24 is [a=c, d=e, foo=bar]
func (m *metadata) mergeParentLabels(lbls labels.Labels, prefixCluster cmtypes.PrefixCluster) {
	m.Lock()
	defer m.Unlock()
	hasCIDR := lbls.HasSource(labels.LabelSourceCIDR) // we should only merge one CIDR label

	// Iterate over all shorter prefixes, from `prefix` to 0.0.0.0/0 // ::/0.
	// Merge all labels, preferring those from longer prefixes, but only merge a single "cidr:XXX" label at most.
	prefix := prefixCluster.AsPrefix()
	for bits := prefix.Bits() - 1; bits >= 0; bits-- {
		parent, _ := prefix.Addr().Unmap().Prefix(bits) // canonical
		if info := m.getLocked(cmtypes.NewPrefixCluster(parent, prefixCluster.ClusterID())); info != nil {
			for k, v := range info.ToLabels() {
				if v.Source == labels.LabelSourceCIDR && hasCIDR {
					continue
				}
				if _, ok := lbls[k]; !ok {
					lbls[k] = v
					if v.Source == labels.LabelSourceCIDR {
						hasCIDR = true
					}
				}
			}
		}
	}
}

// findAffectedChildPrefixes returns the list of all child prefixes which are
// affected by an update to the parent prefix
func (m *metadata) findAffectedChildPrefixes(parent cmtypes.PrefixCluster) (children []cmtypes.PrefixCluster) {
	if parent.IsSingleIP() {
		return []cmtypes.PrefixCluster{parent} // no children
	}

	m.prefixes.Descendants(clusterID(parent.ClusterID()), parent.AsPrefix(), func(child netip.Prefix, _ struct{}) bool {
		children = append(children, cmtypes.NewPrefixCluster(child, parent.ClusterID()))
		return true
	})

	return children
}

// doInjectLabels injects labels from the ipcache metadata (IDMD) map into the
// identities used for the prefixes in the IPCache. The given source is the
// source of the caller, as inserting into the IPCache requires knowing where
// this updated information comes from. Conversely, RemoveLabelsExcluded()
// performs the inverse: removes labels from the IDMD map and releases
// identities allocated by this function.
//
// Note that as this function iterates through the IDMD, if it detects a change
// in labels for a given prefix, then this might allocate a new identity. If a
// prefix was previously associated with an identity, it will get deallocated,
// so a balance is kept, ensuring a one-to-one mapping between prefix and
// identity.
//
// Returns the CIDRs that were not yet processed, for example due to an
// unexpected error while processing the identity updates for those CIDRs
// The caller should attempt to retry injecting labels for those CIDRs.
//
// Do not call this directly; rather, use TriggerLabelInjection()
func (ipc *IPCache) doInjectLabels(ctx context.Context, modifiedPrefixes []cmtypes.PrefixCluster) (remainingPrefixes []cmtypes.PrefixCluster, err error) {
	if ipc.IdentityAllocator == nil {
		return modifiedPrefixes, ErrLocalIdentityAllocatorUninitialized
	}

	if !ipc.Configuration.CacheStatus.Synchronized() {
		return modifiedPrefixes, errors.New("k8s cache not fully synced")
	}

	type ipcacheEntry struct {
		identity      Identity
		tunnelPeer    net.IP
		encryptKey    uint8
		endpointFlags uint8

		force bool
	}

	var (
		// previouslyAllocatedIdentities maps IP Prefix -> Identity for
		// old identities where the prefix will now map to a new identity
		previouslyAllocatedIdentities = make(map[cmtypes.PrefixCluster]Identity)
		// idsToAdd stores the identities that must be updated via the
		// selector cache.
		idsToAdd = make(map[identity.NumericIdentity]labels.LabelArray)
		// entriesToReplace stores the identity to replace in the ipcache.
		entriesToReplace = make(map[cmtypes.PrefixCluster]ipcacheEntry)
		entriesToDelete  = make(map[cmtypes.PrefixCluster]Identity)
		// unmanagedPrefixes is the set of prefixes for which we no longer have
		// any metadata, but were created by a call directly to Upsert()
		unmanagedPrefixes = make(map[cmtypes.PrefixCluster]Identity)
	)

	for i, prefix := range modifiedPrefixes {
		pstr := prefix.String()
		oldID, entryExists := ipc.LookupByIP(pstr)
		oldTunnelIP, oldEncryptionKey := ipc.getHostIPCache(pstr)
		oldEndpointFlags := ipc.getEndpointFlags(pstr)
		prefixInfo := ipc.metadata.get(prefix)
		var newID *identity.Identity
		var isNew bool
		if prefixInfo == nil {
			if !entryExists {
				// Already deleted, no new metadata to associate
				continue
			} // else continue below to remove the old entry
		} else {
			// Insert to propagate the updated set of labels after removal.
			newID, isNew, err = ipc.resolveIdentity(prefix, prefixInfo)
			if err != nil {
				// NOTE: This may fail during a 2nd or later
				// iteration of the loop. To handle this, break
				// the loop here and continue executing the set
				// of changes for the prefixes that were
				// already processed.
				//
				// Old identities corresponding to earlier
				// prefixes may be released as part of this,
				// so hopefully this forward progress will
				// unblock subsequent calls into this function.
				remainingPrefixes = modifiedPrefixes[i:]
				err = fmt.Errorf("failed to allocate new identity during label injection: %w", err)
				break
			}

			var newOverwrittenLegacySource source.Source
			tunnelPeerIP := prefixInfo.TunnelPeer().IP()
			encryptKeyUint8 := prefixInfo.EncryptKey().Uint8()
			epFlagsUint8 := prefixInfo.EndpointFlags().Uint8()
			if entryExists {
				// If an entry already exists for this prefix, then we want to
				// retain its source, if it has been modified by the legacy API.
				// This allows us to restore the original source if we remove all
				// metadata for the prefix
				switch {
				case oldID.exclusivelyOwnedByLegacyAPI():
					// This is the first time we have associated metadata for a
					// modifiedByLegacyAPI=true entry. Store the old (legacy) source:
					newOverwrittenLegacySource = oldID.Source
				case oldID.ownedByLegacyAndMetadataAPI():
					// The entry has modifiedByLegacyAPI=true, but has already been
					// updated at least once by the metadata API. Retain the legacy
					// source as is.
					newOverwrittenLegacySource = oldID.overwrittenLegacySource
				}

				// We can safely skip the ipcache upsert if the entry matches with
				// the entry in the metadata cache exactly.
				// Note that checking ID alone is insufficient, see GH-24502.
				if oldID.ID == newID.ID && prefixInfo.Source() == oldID.Source &&
					oldID.overwrittenLegacySource == newOverwrittenLegacySource &&
					oldTunnelIP.Equal(tunnelPeerIP) &&
					oldEncryptionKey == encryptKeyUint8 &&
					oldEndpointFlags == epFlagsUint8 {
					goto releaseIdentity
				}
			}

			// If this ID was newly allocated, we must add it to the SelectorCache
			if isNew {
				idsToAdd[newID.ID] = newID.Labels.LabelArray()
			}
			entriesToReplace[prefix] = ipcacheEntry{
				identity: Identity{
					ID:                      newID.ID,
					Source:                  prefixInfo.Source(),
					overwrittenLegacySource: newOverwrittenLegacySource,
					// Note: `modifiedByLegacyAPI` and `shadowed` will be
					// set by the upsert call itself
				},
				tunnelPeer:    tunnelPeerIP,
				encryptKey:    encryptKeyUint8,
				endpointFlags: epFlagsUint8,
				// IPCache.Upsert() and friends currently require a
				// Source to be provided during upsert. If the old
				// Source was higher precedence due to labels that
				// have now been removed, then we need to explicitly
				// work around that to remove the old higher-priority
				// identity and replace it with this new identity.
				force: entryExists && prefixInfo.Source() != oldID.Source && oldID.ID != newID.ID,
			}
		}
	releaseIdentity:
		if entryExists {
			// 'prefix' is being removed or modified, so some prior
			// iteration of this loop hit the 'injectLabels' case
			// above, thereby allocating a (new) identity. If we
			// delete or update the identity for 'prefix' in this
			// iteration of the loop, then we must balance the
			// allocation from the prior InjectLabels() call by
			// releasing the previous reference.
			entry, entryToBeReplaced := entriesToReplace[prefix]
			if oldID.exclusivelyOwnedByLegacyAPI() && entryToBeReplaced {
				// If the previous ipcache entry for the prefix
				// was not managed by this function, then the
				// previous ipcache user to inject the IPCache
				// entry retains its own reference to the
				// Security Identity. Given that this function
				// is going to assume (non-exclusive) responsibility
				// for the IPCache entry now, this path must retain its
				// own reference to the Security Identity to
				// ensure that if the other owner ever releases
				// their reference, this reference stays live.
				if option.Config.Debug {
					ipc.logger.Debug(
						"Acquiring Identity reference",
						logfields.IdentityOld, oldID.ID,
						logfields.Identity, entry.identity.ID,
					)
				}
			} else {
				previouslyAllocatedIdentities[prefix] = oldID
			}
			// If all associated metadata for this prefix has been removed,
			// and the existing IPCache entry was never touched by any other
			// subsystem using the old Upsert API, then we can safely remove
			// the IPCache entry associated with this prefix.
			if prefixInfo == nil {
				if oldID.exclusivelyOwnedByMetadataAPI() {
					entriesToDelete[prefix] = oldID
				} else if oldID.ownedByLegacyAndMetadataAPI() {
					// If, on the other hand, this prefix *was* touched by
					// another, Upsert-based system, then we want to restore
					// the original (legacy) source. This ensures that the legacy
					// Delete call (with the legacy source) will be able to remove
					// it.
					unmanagedEntry := ipcacheEntry{
						identity: Identity{
							ID:                  oldID.ID,
							Source:              oldID.overwrittenLegacySource,
							modifiedByLegacyAPI: true,
						},
						tunnelPeer:    oldTunnelIP,
						encryptKey:    oldEncryptionKey,
						endpointFlags: oldEndpointFlags,
						force:         true, /* overwrittenLegacySource is lower precedence */
					}
					entriesToReplace[prefix] = unmanagedEntry

					// In addition, flag this prefix as potentially eligible
					// for deletion if all references are removed (i.e. the legacy
					// Delete call already happened).
					unmanagedPrefixes[prefix] = unmanagedEntry.identity

					if option.Config.Debug {
						ipc.logger.Debug(
							"Previously managed IPCache entry is now unmanaged",
							logfields.IdentityOld, oldID.ID,
						)
					}
				} else if oldID.exclusivelyOwnedByLegacyAPI() {
					// Even if we never actually overwrote the legacy-owned
					// entry, we should still remove it if all references are removed.
					unmanagedPrefixes[prefix] = oldID
				}
			}
		}

		// The reserved:host identity is special: the numeric ID is fixed,
		// and the set of labels is mutable. Thus, whenever it changes,
		// we must always update the SelectorCache (normally, this is elided
		// when no changes are present).
		if newID != nil && newID.ID == identity.ReservedIdentityHost {
			idsToAdd[newID.ID] = newID.Labels.LabelArray()
		}

		// Again, more reserved:host bookkeeping: if this prefix is no longer ID 1 (because
		// it is being deleted or changing IDs), we need to recompute the labels
		// for reserved:host and push that to the SelectorCache
		if entryExists && oldID.ID == identity.ReservedIdentityHost &&
			(newID == nil || newID.ID != identity.ReservedIdentityHost) && prefix.ClusterID() == 0 {
			i := ipc.updateReservedHostLabels(prefix.AsPrefix(), nil)
			idsToAdd[i.ID] = i.Labels.LabelArray()
		}

	}

	// Batch update the SelectorCache and policymaps with the newly allocated identities.
	// This must be done before writing them to the ipcache, or else traffic may be dropped.
	// (This is because prefixes may have identities that are not yet marked as allowed.)
	//
	// We must do this even if we don't appear to have allocated any identities, because they
	// may be in flight due to another caller.
	done := ipc.IdentityUpdater.UpdateIdentities(idsToAdd, nil)
	select {
	case <-done:
	case <-ctx.Done():
		return modifiedPrefixes, ctx.Err()
	}

	ipc.mutex.Lock()
	defer ipc.mutex.Unlock()
	for p, entry := range entriesToReplace {
		prefix := p.String()
		meta := ipc.getK8sMetadata(prefix)
		if _, err2 := ipc.upsertLocked(
			prefix,
			entry.tunnelPeer,
			entry.encryptKey,
			meta,
			entry.identity,
			entry.endpointFlags,
			entry.force,
			/* fromLegacyAPI */ false,
		); err2 != nil {
			// It's plausible to pull the same information twice
			// from different sources, for instance in etcd mode
			// where node information is propagated both via the
			// kvstore and via the k8s control plane. If the
			// upsert was rejected due to source precedence, but the
			// identity is unchanged, then we can safely ignore the
			// error message.
			oldID, ok := previouslyAllocatedIdentities[p]
			if !(ok && oldID.ID == entry.identity.ID && errors.Is(err2, &ErrOverwrite{
				ExistingSrc: oldID.Source,
				NewSrc:      entry.identity.Source,
			})) {
				ipc.logger.Error(
					"Failed to replace ipcache entry with new identity after label removal. Traffic may be disrupted.",
					logfields.Error, err2,
					logfields.IPAddr, prefix,
					logfields.Identity, entry.identity.ID,
				)
			}
		}
	}

	// Delete any no-longer-referenced prefixes.
	// These will now revert to the world identity.
	// This must happen *before* identities are released, or else there will be policy drops
	for prefix, id := range entriesToDelete {
		ipc.deleteLocked(prefix.String(), id.Source)
	}

	// Release our reference for all identities. If their refcount reaches zero, do a
	// sanity check to ensure there are no stale prefixes remaining
	idsToRelease := make([]identity.NumericIdentity, 0, len(previouslyAllocatedIdentities))
	for _, id := range previouslyAllocatedIdentities {
		idsToRelease = append(idsToRelease, id.ID)
	}
	deletedNIDs, err2 := ipc.IdentityAllocator.ReleaseLocalIdentities(idsToRelease...)
	if err2 != nil {
		// should be unreachable, as this only happens if we allocated a global identity
		ipc.logger.Warn("BUG: Failed to release local identity", logfields.Error, err2)
	}

	// Scan all deallocated identities, looking for stale prefixes that still reference them
	for _, deletedNID := range deletedNIDs {
		for prefixStr := range ipc.identityToIPCache[deletedNID] {
			prefix, err := cmtypes.ParsePrefixCluster(prefixStr)
			if err != nil {
				continue // unreachable
			}

			// Corner case: This prefix + identity was initially created by a direct Upsert(),
			// but all identity references have been released. We should then delete this prefix.
			if oldID, unmanaged := unmanagedPrefixes[prefix]; unmanaged && oldID.ID == deletedNID {
				ipc.logger.Debug(
					"Force-removing released prefix from the ipcache.",
					logfields.IPAddr, prefix,
					logfields.Identity, oldID,
				)
				ipc.deleteLocked(prefix.String(), oldID.Source)
			}
		}
	}

	return remainingPrefixes, err
}

// resolveIdentity will either return a previously-allocated identity for the
// given prefix or allocate a new one corresponding to the labels associated
// with the specified prefixInfo.
//
// This function will take an additional reference on the returned identity.
// The caller *must* ensure that this reference is eventually released via
// a call to ipc.IdentityAllocator.Release(). Typically this is tied to whether
// the caller subsequently injects an entry into the BPF IPCache map:
//   - If the entry is inserted, we assume that the entry will eventually be
//     removed, and when it is removed, we will remove that reference from the
//     identity & release the identity.
//   - If the entry is not inserted (for instance, because the bpf IPCache map
//     already has the same IP -> identity entry in the map), immediately release
//     the reference.
func (ipc *IPCache) resolveIdentity(prefix cmtypes.PrefixCluster, info *resourceInfo) (*identity.Identity, bool, error) {
	// Override identities always take precedence
	if info.IdentityOverride() {
		id, isNew, err := ipc.IdentityAllocator.AllocateLocalIdentity(info.ToLabels(), false, identity.InvalidIdentity)
		if err != nil {
			ipc.logger.Warn(
				"Failed to allocate new identity for prefix's IdentityOverrideLabels.",
				logfields.Error, err,
				logfields.ClusterID, prefix.ClusterID(),
				logfields.IPAddr, prefix,
				logfields.Labels, info.ToLabels(),
			)
		}
		return id, isNew, err
	}

	lbls := info.ToLabels()

	// unconditionally merge any parent labels down in to this prefix
	ipc.metadata.mergeParentLabels(lbls, prefix)

	// Enforce certain label invariants, e.g. adding or removing `reserved:world`.
	resolveLabels(lbls, prefix)

	if prefix.ClusterID() == 0 && lbls.HasHostLabel() {
		// Associate any new labels with the host identity.
		//
		// This case is a bit special, because other parts of Cilium
		// have hardcoded assumptions around the host identity and
		// that it corresponds to identity.ReservedIdentityHost.
		// If additional labels are associated with the IPs of the
		// host, add those extra labels into the host identity here
		// so that policy will match on the identity correctly.
		//
		// We can get away with this because the host identity is only
		// significant within the current agent's view (ie each agent
		// will calculate its own host identity labels independently
		// for itself). For all other identities, we avoid modifying
		// the labels at runtime and instead opt to allocate new
		// identities below.
		//
		// As an extra gotcha, we need need to merge all labels for all IPs
		// that resolve to the reserved:host identity, otherwise we can
		// flap identities labels depending on which prefix writes first. See GH-28259.
		i := ipc.updateReservedHostLabels(prefix.AsPrefix(), lbls)
		return i, false, nil
	}

	// This should only ever allocate an identity locally on the node,
	// which could theoretically fail if we ever allocate a very large
	// number of identities.
	id, isNew, err := ipc.IdentityAllocator.AllocateLocalIdentity(lbls, false, info.requestedIdentity.ID())
	if err != nil {
		ipc.logger.Warn(
			"Failed to allocate new identity for prefix's Labels.",
			logfields.Error, err,
			logfields.IPAddr, prefix,
			logfields.Labels, lbls,
		)
		return nil, false, err
	}
	if lbls.HasWorldLabel() {
		id.CIDRLabel = labels.NewLabelsFromModel([]string{labels.LabelSourceCIDR + ":" + prefix.String()})
	}
	return id, isNew, err
}

// resolveLabels applies certain prefix-level invariants to the set of labels.
//
// At a high level, this function makes it so that in-cluster entities
// are not selectable by CIDR and CIDR-equivalent policies.
// This function is necessary as there are a number of *independent* label,
// sources, so only once the full set is computed can we apply this logic.
//
// CIDR and CIDR-equivalent labels are labels with source:
// - cidr:
// - fqdn:
// - cidrgroup:
//
// A prefix with any of these labels is considered "in-cluster"
// - reserved:host
// - reserved:remote-node
// - reserved:health
// - reserved:ingress
//
// However, nodes *are* allowed to be selectable by CIDR and CIDR equivalents
// if PolicyCIDRMatchesNodes() is true.
func resolveLabels(lbls labels.Labels, prefix cmtypes.PrefixCluster) {
	isNode := lbls.HasRemoteNodeLabel() || lbls.HasHostLabel()

	isInCluster := (isNode ||
		lbls.HasHealthLabel() ||
		lbls.HasIngressLabel())

	// In-cluster entities must not have reserved:world.
	if isInCluster {
		lbls.Remove(labels.LabelWorld)
		lbls.Remove(labels.LabelWorldIPv4)
		lbls.Remove(labels.LabelWorldIPv6)
	}

	// In-cluster entities must not have cidr or fqdn labels.
	// Exception: nodes may, when PolicyCIDRMatchesNodes() is enabled.
	if isInCluster && !(isNode && option.Config.PolicyCIDRMatchesNodes()) {
		lbls.RemoveFromSource(labels.LabelSourceCIDR)
		lbls.RemoveFromSource(labels.LabelSourceFQDN)
		lbls.RemoveFromSource(labels.LabelSourceCIDRGroup)
	}

	// Remove all labels with source `node:`, unless this is a node *and* node labels are enabled.
	if !(isNode && option.Config.PerNodeLabelsEnabled()) {
		lbls.RemoveFromSource(labels.LabelSourceNode)
	}

	// No empty labels allowed.
	// Add in (cidr:<address/prefix>) label as a fallback.
	// This should not be hit in production, but is used in tests.
	if len(lbls) == 0 {
		maps.Copy(lbls, labels.GetCIDRLabels(prefix.AsPrefix()))
	}

	// add world if not in-cluster.
	if !isInCluster {
		lbls.AddWorldLabel(prefix.AsPrefix().Addr())
	}
}

// updateReservedHostLabels adds or removes labels that apply to the local host.
// The `reserved:host` identity is special: the numeric identity is fixed
// and the set of labels is mutable. (The datapath requires this.) So,
// we need to determine all prefixes that have the `reserved:host` label and
// capture their labels. Then, we must aggregate *all* labels from all prefixes and
// update the labels that correspond to the `reserved:host` identity.
//
// This could be termed a meta-ipcache. The ipcache metadata layer aggregates
// an arbitrary set of resources and labels to a prefix. Here, we are aggregating an arbitrary
// set of prefixes and labels to an identity.
func (ipc *IPCache) updateReservedHostLabels(prefix netip.Prefix, lbls labels.Labels) *identity.Identity {
	ipc.metadata.reservedHostLock.Lock()
	defer ipc.metadata.reservedHostLock.Unlock()
	if lbls == nil {
		delete(ipc.metadata.reservedHostLabels, prefix)
	} else {
		ipc.metadata.reservedHostLabels[prefix] = lbls
	}

	// aggregate all labels and update static identity
	newLabels := labels.NewFrom(labels.LabelHost)
	for _, l := range ipc.metadata.reservedHostLabels {
		newLabels.MergeLabels(l)
	}

	ipc.logger.Debug(
		"Merged labels for reserved:host identity",
		logfields.Labels, newLabels,
	)

	return identity.AddReservedIdentityWithLabels(identity.ReservedIdentityHost, newLabels)
}

// appendAPIServerLabelsForDeletion inspects labels and performs special handling for corner cases like API server entities
// deployed external to the cluster.
func appendAPIServerLabelsForDeletion(lbls labels.Labels, currentLabels labels.Labels) labels.Labels {
	if currentLabels.HasKubeAPIServerLabel() && currentLabels.HasWorldLabel() && len(currentLabels) == 2 {
		lbls.MergeLabels(labels.LabelWorld)
	}
	return lbls
}

// RemoveLabelsExcluded removes the given labels from all IPs inside the IDMD
// except for the IPs / prefixes inside the given excluded set.
//
// The caller must subsequently call IPCache.TriggerLabelInjection() to push
// these changes down into the policy engine and ipcache datapath maps.
func (ipc *IPCache) RemoveLabelsExcluded(
	lbls labels.Labels,
	toExclude map[cmtypes.PrefixCluster]struct{},
	rid types.ResourceID,
) {
	ipc.metadata.Lock()
	defer ipc.metadata.Unlock()

	var affectedPrefixes []cmtypes.PrefixCluster
	oldSet := ipc.metadata.filterByLabels(lbls)
	for _, ip := range oldSet {
		if _, ok := toExclude[ip]; !ok {
			prefixLabels := ipc.metadata.getLocked(ip).ToLabels()
			lblsToRemove := appendAPIServerLabelsForDeletion(lbls, prefixLabels)
			affectedPrefixes = append(affectedPrefixes, ipc.metadata.remove(ip, rid, lblsToRemove)...)
		}
	}
	ipc.metadata.enqueuePrefixUpdates(affectedPrefixes...)
}

// filterByLabels returns all the prefixes inside the ipcache metadata map
// which contain the given labels. Note that `filter` is a subset match, not a
// full match.
//
// Assumes that the ipcache metadata read lock is taken!
func (m *metadata) filterByLabels(filter labels.Labels) []cmtypes.PrefixCluster {
	var matching []cmtypes.PrefixCluster
	sortedFilter := filter.SortedList()
	for prefix := range m.m {
		lbls := m.getLocked(prefix).ToLabels()
		if bytes.Contains(lbls.SortedList(), sortedFilter) {
			matching = append(matching, prefix)
		}
	}
	return matching
}

// remove asynchronously removes the labels association for a prefix.
//
// This function assumes that the ipcache metadata lock is held for writing.
func (m *metadata) remove(prefix cmtypes.PrefixCluster, resource types.ResourceID, aux ...IPMetadata) []cmtypes.PrefixCluster {
	prefix = canonicalPrefix(prefix)
	info, ok := m.m[prefix]
	if !ok || info.byResource[resource] == nil {
		return nil
	}

	// compute affected prefixes before deletion, to ensure the prefix matches
	// its own entry before it is deleted
	affected := m.findAffectedChildPrefixes(prefix)

	for _, a := range aux {
		info.byResource[resource].unmerge(m.logger, a)
	}
	if !info.byResource[resource].isValid() {
		delete(info.byResource, resource)
	}
	if !info.isValid() { // Labels empty, delete
		delete(m.m, prefix)
		m.prefixes.Delete(clusterID(prefix.ClusterID()), prefix.AsPrefix())
	} else {
		// erase flattened, we'll recompute on read
		info.flattened = nil
	}

	return affected
}

// TriggerLabelInjection triggers the label injection controller to iterate
// through the IDMD and potentially allocate new identities based on any label
// changes.
//
// The following diagram describes the relationship between the label injector
// triggered here and the callers/callees.
//
//	+------------+  (1)        (1)  +-----------------------------+
//	| EP Watcher +-----+      +-----+ CN Watcher / Node Discovery |
//	+-----+------+   W |      | W   +------+----------------------+
//	      |            |      |            |
//	      |            v      v            |
//	      |            +------+            |
//	      |            | IDMD |            |
//	      |            +------+            |
//	      |               ^                |
//	      |               |                |
//	      |           (3) |R               |
//	      | (2)    +------+--------+   (2) |
//	      +------->|Label Injector |<------+
//	     Trigger   +-------+-------+ Trigger
//		      (4) |W    (5) |W
//		          |         |
//		          v         v
//		     +--------+   +---+
//		     |Policy &|   |IPC|
//		     |datapath|   +---+
//		     +--------+
//	legend:
//	* W means write
//	* R means read
func (ipc *IPCache) TriggerLabelInjection() {
	// GH-17829: Would also be nice to have an end-to-end test to validate
	//           on upgrade that there are no connectivity drops when this
	//           channel is preventing transient BPF entries.

	// This controller is for retrying this operation in case it fails. It
	// should eventually succeed.
	ipc.injectionStarted.Do(func() {
		ipc.UpdateController(
			LabelInjectorName,
			controller.ControllerParams{
				Group:            injectLabelsControllerGroup,
				Context:          ipc.Configuration.Context,
				DoFunc:           ipc.handleLabelInjection,
				MaxRetryInterval: 1 * time.Minute,
			},
		)
	})
	ipc.controllers.TriggerController(LabelInjectorName)
}

// Changeable just for unit tests.
var chunkSize = 512

// handleLabelInjection dequeues the set of pending prefixes and processes
// their metadata updates
func (ipc *IPCache) handleLabelInjection(ctx context.Context) error {
	if ipc.Configuration.CacheStatus != nil {
		// wait for k8s caches to sync.
		// this is duplicated from doInjectLabels(), but it keeps us from needlessly
		// churning the queue while the agent initializes.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ipc.Configuration.CacheStatus:
		}
	}

	// Any prefixes that have failed and must be retried
	var retry []cmtypes.PrefixCluster
	var err error

	idsToModify, rev := ipc.metadata.dequeuePrefixUpdates()

	cs := chunkSize
	// no point in dividing for the first run, we will not be releasing any identities anyways.
	if rev == 1 {
		cs = len(idsToModify)
	}

	// Split ipcache updates in to chunks to reduce resource spikes.
	// InjectLabels releases all identities only at the end of processing, so
	// it may allocate up to `chunkSize` additional identities.
	for len(idsToModify) > 0 {
		idx := min(len(idsToModify), cs)
		chunk := idsToModify[0:idx]
		idsToModify = idsToModify[idx:]

		var failed []cmtypes.PrefixCluster

		// If individual prefixes failed injection, doInjectLabels() the set of failed prefixes
		// and sets err. We must ensure the failed prefixes are re-queued for injection.
		failed, err = ipc.doInjectLabels(ctx, chunk)
		retry = append(retry, failed...)
		if err != nil {
			break
		}
	}

	ok := true
	if len(retry) > 0 {
		// err will also be set, so
		ipc.metadata.enqueuePrefixUpdates(retry...)
		ok = false
	}
	if len(idsToModify) > 0 {
		ipc.metadata.enqueuePrefixUpdates(idsToModify...)
		ok = false
	}
	if ok {
		// if all prefixes were successfully injected, bump the revision
		// so that any waiters are made aware.
		ipc.metadata.setInjectedRevision(rev)
	}

	// non-nil err will re-trigger this controller
	return err
}
