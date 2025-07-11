// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package k8s

import (
	"context"
	"regexp"
	"strings"

	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/time"
)

var (

	// k8sErrMsgMU guards additions and removals to k8sErrMsg, which stores a
	// time after which a repeat error message can be printed
	k8sErrMsgMU lock.Mutex
	k8sErrMsg   = map[string]time.Time{}
)

const k8sErrLogTimeout = time.Minute

// k8sErrorUpdateCheckUnmuteTime returns a boolean indicating whether we should
// log errmsg or not. It manages once-per-k8sErrLogTimeout entry in k8sErrMsg.
// When errmsg is new or more than k8sErrLogTimeout has passed since the last
// invocation that returned true, it returns true.
func k8sErrorUpdateCheckUnmuteTime(errstr string, now time.Time) bool {
	k8sErrMsgMU.Lock()
	defer k8sErrMsgMU.Unlock()

	if unmuteDeadline, ok := k8sErrMsg[errstr]; !ok || now.After(unmuteDeadline) {
		k8sErrMsg[errstr] = now.Add(k8sErrLogTimeout)
		return true
	}

	return false
}

var k8sObjDecodeErrRe = regexp.MustCompile("invalid character.*looking for beginning of value")

// K8sErrorHandler handles the error messages in a non verbose way by omitting
// repeated instances of the same error message for a timeout defined with
// k8sErrLogTimeout.
func K8sErrorHandler(_ context.Context, e error, _ string, _ ...any) {
	if e == nil {
		return
	}

	// slogloggercheck: it's safe to use the default logger here as it has been initialized by the program up to this point.
	logger := logging.DefaultSlogLogger

	// We rate-limit certain categories of error message. These are matched
	// below, with a default behaviour to print everything else without
	// rate-limiting.
	// Note: We also have side-effects in some of the special cases.
	now := time.Now()
	errstr := e.Error()
	switch {
	// This can occur when cilium comes up before the k8s API server, and keeps
	// trying to connect.
	case strings.Contains(errstr, "connection refused"):
		if k8sErrorUpdateCheckUnmuteTime(errstr, now) {
			logger.Error("k8sError", logfields.Error, e)
		}

	// k8s does not allow us to watch both ThirdPartyResource and
	// CustomResourceDefinition. This would occur when a user mixes these within
	// the k8s cluster, and might occur when upgrading from versions of cilium
	// that used ThirdPartyResource to define CiliumNetworkPolicy.
	case strings.Contains(errstr, "Failed to list *v2.CiliumNetworkPolicy: the server could not find the requested resource"):
		if k8sErrorUpdateCheckUnmuteTime(errstr, now) {
			logger.Error("No Cilium Network Policy CRD defined in the cluster, please set `--skip-crd-creation=false` to avoid seeing this error.", logfields.Error, e)
		}

	// fromCIDR and toCIDR used to expect an "ip" subfield (so, they were a YAML
	// map with one field) but common usage and expectation would simply list the
	// CIDR ranges and IPs desired as a YAML list. In these cases we would see
	// this decode error. We have since changed the definition to be a simple
	// list of strings.
	case strings.Contains(errstr, "Unable to decode an event from the watch stream: unable to decode watch event"),
		strings.Contains(errstr, "Failed to list *v1.CiliumNetworkPolicy: only encoded map or array can be decoded into a struct"),
		strings.Contains(errstr, "Failed to list *v2.CiliumNetworkPolicy: only encoded map or array can be decoded into a struct"),
		strings.Contains(errstr, "Failed to list *v2.CiliumNetworkPolicy: v2.CiliumNetworkPolicyList:"):
		if k8sErrorUpdateCheckUnmuteTime(errstr, now) {
			logger.Error("Unable to decode k8s watch event", logfields.Error, e)
		}

	case k8sObjDecodeErrRe.MatchString(errstr):
		logger.Error("K8s client-go error indicates failure to decode k8s objs from apiserver."+
			" This likely indicate issues with k8s apiserver sending malformed data or errors."+
			" Such issues may be related to corrupted k8s etcd state.",
			logfields.Error, e,
		)
	default:
		logger.Error("k8sError", logfields.Error, e)
	}
}
