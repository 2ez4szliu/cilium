.. only:: not (epub or latex or html)

    WARNING: You are looking at unreleased Cilium documentation.
    Please use the official rendered version released here:
    https://docs.cilium.io

.. _admin_upgrade:

*************
Upgrade Guide
*************

.. _upgrade_general:

This upgrade guide is intended for Cilium running on Kubernetes. If you have
questions, feel free to ping us on `Cilium Slack`_.

.. include:: upgrade-warning.rst

.. _pre_flight:

Running pre-flight check (Required)
===================================

When rolling out an upgrade with Kubernetes, Kubernetes will first terminate the
pod followed by pulling the new image version and then finally spin up the new
image. In order to reduce the downtime of the agent and to prevent ``ErrImagePull``
errors during upgrade, the pre-flight check pre-pulls the new image version.
If you are running in :ref:`kubeproxy-free`
mode you must also pass on the Kubernetes API Server IP and /
or the Kubernetes API Server Port when generating the ``cilium-preflight.yaml``
file.

.. tabs::
  .. group-tab:: kubectl

    .. parsed-literal::

      helm template |CHART_RELEASE| \\
        --namespace=kube-system \\
        --set preflight.enabled=true \\
        --set agent=false \\
        --set operator.enabled=false \\
        > cilium-preflight.yaml
      kubectl create -f cilium-preflight.yaml

  .. group-tab:: Helm

    .. parsed-literal::

      helm install cilium-preflight |CHART_RELEASE| \\
        --namespace=kube-system \\
        --set preflight.enabled=true \\
        --set agent=false \\
        --set operator.enabled=false

  .. group-tab:: kubectl (kubeproxy-free)

    .. parsed-literal::

      helm template |CHART_RELEASE| \\
        --namespace=kube-system \\
        --set preflight.enabled=true \\
        --set agent=false \\
        --set operator.enabled=false \\
        --set k8sServiceHost=API_SERVER_IP \\
        --set k8sServicePort=API_SERVER_PORT \\
        > cilium-preflight.yaml
      kubectl create -f cilium-preflight.yaml

  .. group-tab:: Helm (kubeproxy-free)

    .. parsed-literal::

      helm install cilium-preflight |CHART_RELEASE| \\
        --namespace=kube-system \\
        --set preflight.enabled=true \\
        --set agent=false \\
        --set operator.enabled=false \\
        --set k8sServiceHost=API_SERVER_IP \\
        --set k8sServicePort=API_SERVER_PORT

After applying the ``cilium-preflight.yaml``, ensure that the number of READY
pods is the same number of Cilium pods running.

.. code-block:: shell-session

    $ kubectl get daemonset -n kube-system | sed -n '1p;/cilium/p'
    NAME                      DESIRED   CURRENT   READY   UP-TO-DATE   AVAILABLE   NODE SELECTOR   AGE
    cilium                    2         2         2       2            2           <none>          1h20m
    cilium-pre-flight-check   2         2         2       2            2           <none>          7m15s

Once the number of READY pods are equal, make sure the Cilium pre-flight
deployment is also marked as READY 1/1. If it shows READY 0/1, consult the
:ref:`cnp_validation` section and resolve issues with the deployment before
continuing with the upgrade.

.. code-block:: shell-session

    $ kubectl get deployment -n kube-system cilium-pre-flight-check -w
    NAME                      READY   UP-TO-DATE   AVAILABLE   AGE
    cilium-pre-flight-check   1/1     1            0           12s

.. _cleanup_preflight_check:

Clean up pre-flight check
-------------------------

Once the number of READY for the preflight :term:`DaemonSet` is the same as the number
of cilium pods running and the preflight ``Deployment`` is marked as READY ``1/1``
you can delete the cilium-preflight and proceed with the upgrade.

.. tabs::
  .. group-tab:: kubectl

    .. code-block:: shell-session

      kubectl delete -f cilium-preflight.yaml

  .. group-tab:: Helm

    .. code-block:: shell-session

      helm delete cilium-preflight --namespace=kube-system

.. _upgrade_minor:

Upgrading Cilium
================

During normal cluster operations, all Cilium components should run the same
version. Upgrading just one of them (e.g., upgrading the agent without
upgrading the operator) could result in unexpected cluster behavior.
The following steps will describe how to upgrade all of the components from
one stable release to a later stable release.

.. include:: upgrade-warning.rst

Step 1: Upgrade to latest patch version
---------------------------------------

When upgrading from one minor release to another minor release, for example
1.x to 1.y, it is recommended to upgrade to the `latest patch release
<https://github.com/cilium/cilium#stable-releases>`__ for a Cilium release series first.
Upgrading to the latest patch release ensures the most seamless experience if a
rollback is required following the minor release upgrade. The upgrade guides
for previous versions can be found for each minor version at the bottom left
corner.

Step 2: Use Helm to Upgrade your Cilium deployment
--------------------------------------------------------------------------------------

:term:`Helm` can be used to either upgrade Cilium directly or to generate a new set of
YAML files that can be used to upgrade an existing deployment via ``kubectl``.
By default, Helm will generate the new templates using the default values files
packaged with each new release. You still need to ensure that you are
specifying the equivalent options as used for the initial deployment, either by
specifying a them at the command line or by committing the values to a YAML
file.

.. include:: ../installation/k8s-install-download-release.rst

To minimize datapath disruption during the upgrade, the
``upgradeCompatibility`` option should be set to the initial Cilium
version which was installed in this cluster.

.. tabs::
  .. group-tab:: kubectl

    Generate the required YAML file and deploy it:

    .. parsed-literal::

      helm template |CHART_RELEASE| \\
        --set upgradeCompatibility=1.X \\
        --namespace kube-system \\
        > cilium.yaml
      kubectl apply -f cilium.yaml

  .. group-tab:: Helm

    Deploy Cilium release via Helm:

    .. parsed-literal::

      helm upgrade cilium |CHART_RELEASE| \\
        --namespace=kube-system \\
        --set upgradeCompatibility=1.X

.. note::

   Instead of using ``--set``, you can also save the values relative to your
   deployment in a YAML file and use it to regenerate the YAML for the latest
   Cilium version. Running any of the previous commands will overwrite
   the existing cluster's :term:`ConfigMap` so it is critical to preserve any existing
   options, either by setting them at the command line or storing them in a
   YAML file, similar to:

   .. code-block:: yaml

      agent: true
      upgradeCompatibility: "1.8"
      ipam:
        mode: "kubernetes"
      k8sServiceHost: "API_SERVER_IP"
      k8sServicePort: "API_SERVER_PORT"
      kubeProxyReplacement: "true"

   You can then upgrade using this values file by running:

   .. parsed-literal::

      helm upgrade cilium |CHART_RELEASE| \\
        --namespace=kube-system \\
        -f my-values.yaml

When upgrading from one minor release to another minor release using
``helm upgrade``, do *not* use Helm's ``--reuse-values`` flag.
The ``--reuse-values`` flag ignores any newly introduced values present in
the new release and thus may cause the Helm template to render incorrectly.
Instead, if you want to reuse the values from your existing installation,
save the old values in a values file, check the file for any renamed or
deprecated values, and then pass it to the ``helm upgrade`` command as
described above. You can retrieve and save the values from an existing
installation with the following command:

.. code-block:: shell-session

  helm get values cilium --namespace=kube-system -o yaml > old-values.yaml

The ``--reuse-values`` flag may only be safely used if the Cilium chart version
remains unchanged, for example when ``helm upgrade`` is used to apply
configuration changes without upgrading Cilium.

Step 3: Rolling Back
--------------------

Occasionally, it may be necessary to undo the rollout because a step was missed
or something went wrong during upgrade. To undo the rollout run:

.. tabs::
  .. group-tab:: kubectl

    .. code-block:: shell-session

      kubectl rollout undo daemonset/cilium -n kube-system

  .. group-tab:: Helm

    .. code-block:: shell-session

      helm history cilium --namespace=kube-system
      helm rollback cilium [REVISION] --namespace=kube-system

This will revert the latest changes to the Cilium ``DaemonSet`` and return
Cilium to the state it was in prior to the upgrade.

.. note::

    When rolling back after new features of the new minor version have already
    been consumed, consult the :ref:`version_notes` to check and prepare for
    incompatible feature use before downgrading/rolling back. This step is only
    required after new functionality introduced in the new minor version has
    already been explicitly used by creating new resources or by opting into
    new features via the :term:`ConfigMap`.

.. _version_notes:
.. _upgrade_version_specifics:

Version Specific Notes
======================

This section details the upgrade notes specific to |CURRENT_RELEASE|. Read them
carefully and take the suggested actions before upgrading Cilium to |CURRENT_RELEASE|.
For upgrades to earlier releases, see the
:prev-docs:`upgrade notes to the previous version <operations/upgrade/#upgrade-notes>`.

The only tested upgrade and rollback path is between consecutive minor releases.
Always perform upgrades and rollbacks between one minor release at a time.
Additionally, always update to the latest patch release of your current version
before attempting an upgrade.

Tested upgrades are expected to have minimal to no impact on new and existing
connections matched by either no Network Policies, or L3/L4 Network Policies only.
Any traffic flowing via user space proxies (for example, because an L7 policy is
in place, or using Ingress/Gateway API) will be disrupted during upgrade. Endpoints
communicating via the proxy must reconnect to re-establish connections.

.. _current_release_required_changes:

.. _1.18_upgrade_notes:

1.18 Upgrade Notes
------------------
* ``cilium-dbg bpf policy`` now prints ``ANY`` and not ``reserved:unknown`` for a bpf policy entry that allows any peer identity.
* The ``v2alpha1`` version of ``CiliumBGPClusterConfig``, ``CiliumBGPPeerConfig``, ``CiliumBGPAdvertisement``, ``CiliumBGPNodeConfig`` and
  ``CiliumBGPNodeConfigOverride`` CRDs was deprecated in favor of the ``v2`` version. Change ``apiVersion: cilium.io/v2alpha1``
  to ``apiVersion: cilium.io/v2`` for these CRDs in all your BGP configs. The previously deprecated field
  ``spec.transport.localPort`` in ``CiliumBGPPeerConfig`` has been removed and will be ignored if it was configured in the ``v2alpha1`` version.
* The ``CiliumBGPPeeringPolicy`` CRD is deprecated and will be removed in a future release. Please migrate to ``cilium.io/v2``
  BGP CRDs (``CiliumBGPClusterConfig``, ``CiliumBGPPeerConfig``, ``CiliumBGPAdvertisement``, ``CiliumBGPNodeConfigOverride``) to configure BGP.
* The ``v2alpha1`` version of ``CiliumCIDRGroup`` CRD was deprecated in favor of the ``v2`` version. Change ``apiVersion: cilium.io/v2alpha1``
  to ``apiVersion: cilium.io/v2`` for all ``CiliumCIDRGroup`` resources.
* The check for connectivity to the Kubernetes apiserver has been removed from the cilium-agent liveness probe. This can be turned back on
  by setting the helm option ``livenessProbe.requireK8sConnectivity`` to ``true``.
* The label ``io.cilium.k8s.policy.serviceaccount`` will be included in the default label list. If you configure your own identity-relevant labels 
  on your cluster, the number of identities will temporarily increase during the upgrade, which will result in increased drops. If you would like 
  to disable this new behavior, you can add ``!io\.cilium\.k8s\.policy\.serviceaccount`` to your identity-relevant labels to 
  exclude the ``io.cilium.k8s.policy.serviceaccount`` label.
* If using IPsec encryption the upgrade from v1.17 to v1.18 requires special attention.
  Please reference :ref:`encryption_ipsec`.
* If using an IPsec deployment within a Google Cloud GKE cluster the default firewall rules for the cluster's subnet
  must be updated to allow ESP traffic.
  See :ref:`encryption_ipsec` for details.
* The Helm value of ``enableIPv4Masquerade`` in ``eni`` mode changes from ``true`` to ``false`` by default from 1.18.
  To keep the ``enableIPv4Masquerade`` enabled, explicitly set the value for
  this option to ``true``, or use a value strictly lower than 1.18 for
  ``upgradeCompatibility``.
* This Cilium version now requires a v5.10 Linux kernel or newer.
* CiliumIdentity CRD does not contain Security Labels in metadata anymore except for the namespace label.
* The support for Envoy Go Extensions (proxylib) is deprecated, and will be removed in a future release.
* The kube_proxy_healthz endpoint no longer requires Kubernetes control plane connectivity to succeed.
* In a Cluster Mesh environment, network policy ingress and egress selectors currently select by default
  endpoints from all clusters unless one or more clusters are explicitly specified in the policy itself.
  The new ``policy-default-local-cluster`` flag allows to change this behavior, and only select endpoints
  from the local cluster, unless explicitly specified, to improve the default security posture.
  This option is intended to become the default in Cilium v1.19. If you are using Cilium ClusterMesh and network policies,
  you need to take action to update your network policies to avoid this change from breaking connectivity for applications
  across different clusters. There is no need to do anything for the Cilium 1.17 to 1.18 upgrade, but it is strongly
  recommended to check :ref:`change_policy_default_local_cluster` for details and migration recommendations to update
  your network policies in advance for the Cilium 1.19 upgrade.
* Creating or deleting policies via the local REST api is deprecated. This will be removed entirely in v1.19.

Removed Options
~~~~~~~~~~~~~~~

* The previously deprecated high-scale mode for ipcache has been removed.
* The previously deprecated hubble-relay flag ``--dial-timeout`` has been removed.
* The previously deprecated External Workloads feature has been removed. To remove stale resources, run ``kubectl delete crd ciliumexternalworkloads.cilium.io``. In addition, you might want to delete a K8s secret used by External Workloads. Run ``kubectl -n kube-system get secrets`` to find one.
* The previously deprecated ``--datapath-mode=lb-only`` for plain Docker mode has been removed.
* The ``update-ec2-adapter-limit-via-api`` CLI flag for the operator has been removed since the operator will only and always use the
  EC2API to update the EC2 instance limit.
* The ``aws-instance-limit-mapping`` CLI flag for the operator has been removed since the operator will only and always use the
  EC2API to update the EC2 instance limit.
* The previously deprecated flag ``--enable-k8s-terminating-endpoint`` has been removed.
  The K8s terminating endpoints feature is unconditionally enabled.
* The previously deprecated ``CONNTRACK_LOCAL`` option has been removed
* The previously deprecated ``enableRuntimeDeviceDetection`` option has been removed
* The previously deprecated and ignored operator flags ``ces-write-qps-limit``, ``ces-write-qps-burst``, ``ces-enable-dynamic-rate-limit``,
  ``ces-dynamic-rate-limit-nodes``, ``ces-dynamic-rate-limit-qps-limit``, ``ces-dynamic-rate-limit-qps-burst`` have been removed.
* The ``arping-refresh-period`` option has been removed. Cilium will now refresh neighbor entries based on the ``base_reachable_time_ms`` sysctl value associated with that entry.

Deprecated Options
~~~~~~~~~~~~~~~~~~

* Operator flag ``ces-slice-mode`` has been deprecated and will be removed in Cilium 1.19.
  CiliumEndpointSlice batching mode defaults to first-come-first-serve mode.
* The flag value ``--datapath-mode=lb-only`` for plain Docker mode has been migrated into
  ``--bpf-lb-only`` and will be removed in Cilium 1.19.
* ``k8s-api-server``: This option has been deprecated in favor of ``k8s-api-server-urls``
  and will be removed in Cilium 1.19.
* ``--l2-pod-announcements-interface`` has been deprecated in favor of
  ``--l2-pod-announcements-interface-pattern`` and will be removed in Cilium 1.19.
* The flag ``--enable-session-affinity`` (``sessionAffinity`` in Helm) has been deprecated and will be removed in Cilium 1.19.
  The Session Affinity feature will be unconditionally enabled. Also, in Cilium 1.18, the
  feature is enabled by default.
* The custom calls feature (``--enable-custom-calls``) has been deprecated, and will
  be removed in Cilium 1.19.
* The flag ``--bpf-lb-proto-diff`` has been deprecated and will be removed in Cilium 1.19.
  Service protocol differentiation will be unconditionally enabled.
* The flags ``--enable-recorder``, ``--enable-hubble-recorder-api``, ``--hubble-recorder-storage-path``
  and ``--hubble-recorder-sink-queue-size`` have been deprecated. The Hubble Recorder feature will be
  removed in Cilium 1.19.
  You can use `pwru <https://github.com/cilium/pwru>`_ with ``--filter-trace-xdp`` to trace XDP requests.
* The flags ``--enable-node-port`` (``nodePort.enabled`` in Helm), ``--enable-host-port``, ``--enable-external-ips`` have been deprecated
  and will be removed in Cilium 1.19. The kube-proxy replacement features will be only enabled when
  ``--kube-proxy-replacent`` is set to ``true``.
* The flag ``--enable-k8s-endpoint-slice`` have been deprecated and will be removed in Cilium 1.19.
  The K8s Endpoint Slice feature will be unconditionally enabled.
* The flag ``--enable-internal-traffic-policy`` (``enableInternalTrafficPolicy`` in Helm) has been deprecated and will be removed in Cilium 1.19. The
  ``internalTrafficPolicy`` field in a Kubernetes Service object will be unconditionally respected.
* The flag ``--enable-svc-source-range-check`` (``svcSourceRangeCheck`` in Helm) has been deprecated
  and will be removed in Cilium 1.19. The feature will be enabled automatically when ``--kube-proxy-replacent``
  is set to ``true``.
* The flag ``--egress-multi-home-ip-rule-compat`` and the old IP rule scheme has been deprecated and will be removed
  in Cilium 1.19. Running Cilium 1.18 with the flag set to ``false`` (default value) will migrate any existing IP rules
  to the new scheme.
* The flag ``--enable-ipv4-egress-gateway`` has been deprecated in favor of ``--enable-egress-gateway`` and will
  be removed in Cilium 1.19.

Helm Options
~~~~~~~~~~~~

* The Helm options ``hubble.export.fileMaxSizeMb``, ``hubble.export.fileMaxBackups``
  and ``hubble.export.fileCompress`` have been deprecated in favor of their corresponding exporter
  type options and will be removed in Cilium 1.19. More specifically, the static exporter options
  are now located under ``hubble.export.static`` and the dynamic exporter options that generate
  a configmap containing the exporter configuration are now under ``hubble.export.dynamic.config.content``.
* The Helm option ``ciliumEndpointSlice.sliceMode`` has been removed. The slice mode defaults to first-come-first-serve mode.
* The Helm chart now defaults to enabling exponential backoff for client-go by setting the environment variables
  ``KUBE_CLIENT_BACKOFF_BASE`` and ``KUBE_CLIENT_BACKOFF_DURATION`` on the Cilium daemonset.
  These can be customized using helm values ``k8sClientExponentialBackoff.backoffBaseSeconds`` and
  ``k8sClientExponentialBackoff.backoffMaxDurationSeconds``. Users who were already setting these
  using ``extraEnv`` should either remove them from ``extraEnv`` or set ``k8sClientExponentialBackoff.enabled=false``.
* The deprecated Helm option ``hubble.relay.dialTimeout`` has been removed.
* The new Helm option ``underlayProtocol`` allows selecting the IP family for the underlay. It defaults to IPv4.
* ``k8s.apiServerURLs`` has been introduced to specify multiple Kubernetes API servers so that the agent can fail over
  to an active instance.
* ``eni.updateEC2AdapterLimitViaAPI`` is removed since the operator will only and always use the EC2API to update the EC2 instance limit.
* The Helm option ``l2PodAnnouncements.interface`` has been deprecated in favor of ``l2PodAnnouncements.interfacePattern``
  and will be removed in Cilium 1.19.
* The Helm value of ``enableIPv4Masquerade`` in ``eni`` mode changes from ``true`` to ``false`` by default from 1.18.
* The Helm option ``clustermesh.apiserver.kvstoremesh.enabled`` has been deprecated and will be removed in Cilium 1.19.
  Starting from 1.19 KVStoreMesh will be unconditionally enabled when the Cluster Mesh API Server is enabled.
* The ``l2NeighDiscovery.refreshPeriod`` option has been removed. Cilium will now refresh neighbor entries based on the ``base_reachable_time_ms`` sysctl value associated with that entry.
* The ``l2NeighDiscovery.enabled`` option has been changed to default to ``false``.
* The deprecated Helm option ``enableCiliumEndpointSlice`` has been removed. Set
  ``ciliumEndpointSlice.enabled`` instead to enable CiliumEndpointSlices.
* ``localRedirectPolicy`` helm option has been deprecated. Set ``localRedirectPolicies.enabled`` instead.
* The new ``localRedirectPolicies.addressMatcherCIDRs`` option can be used to limit what addresses are allowed in an address match of a CiliumLocalRedirectPolicy.
* The default value for ``operator.tolerations`` has been narrowed to only include the following tolerations:
   ``node-role.kubernetes.io/control-plane`` , ``node-role.kubernetes.io/master`` , ``node.kubernetes.io/not-ready`` and ``node.cilium.io/agent-not-ready``. This will 
   block the operator running on drained nodes. 

Agent Options
~~~~~~~~~~~~~

* The new agent flag ``underlay-protocol`` allows selecting the IP family for the underlay. It defaults to IPv4.
* ``k8s-api-server-urls``: This option specifies a list of URLs for Kubernetes API server instances to support high availability
  for the servers. The agent will fail over to an active instance in case of connectivity failures at runtime.
* The ``--enable-l2-neigh-discovery`` flag has been changed to default to ``false``.
* The ``kvstore-connectivity-timeout`` flag is renamed to ``identity-allocation-timeout`` to better reflect its purpose.
* The ``kvstore-periodic-sync`` flag is renamed to ``identity-allocation-sync-interval`` to better reflect its purpose.

Cluster Mesh API Server Options
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

* The previously unused ``kvstore-connectivity-timeout`` and ``kvstore-periodic-sync``
  flags have been removed from the apiserver and kvstoremesh commands.

Bugtool Options
~~~~~~~~~~~~~~~

* The deprecated flag ``k8s-mode`` (and related flags ``cilium-agent-container-name``, ``k8s-namespace`` & ``k8s-label``)
  have been removed. Cilium CLI should be used to gather a sysdump from a K8s cluster.

Added Metrics
~~~~~~~~~~~~~

Removed Metrics
~~~~~~~~~~~~~~~

The following deprecated metrics were removed:

* ``node_connectivity_status``
* ``node_connectivity_latency_seconds``

Changed Metrics
~~~~~~~~~~~~~~~

* ``doublewrite_identity_crd_total_count`` has been renamed to ``doublewrite_crd_identities``
* ``doublewrite_identity_kvstore_total_count`` has been renamed to ``doublewrite_kvstore_identities``
* ``doublewrite_identity_crd_only_count`` has been renamed to ``doublewrite_crd_only_identities``
* ``doublewrite_identity_kvstore_only_count`` has been renamed to ``doublewrite_kvstore_only_identities``
* The type of the ``cilium_agent_bootstrap_seconds`` metric has been changed from histogram to gauge.
* ``cilium_agent_bgp_control_plane_reconcile_error_count`` has been renamed to ``cilium_agent_bgp_control_plane_reconcile_errors_total``.
* ``cilium_operator_bgp_control_plane_cluster_config_error_count`` has been renamed to ``cilium_operator_bgp_control_plane_reconcile_errors_total``
  and its label ``bgp_cluster_config`` has been replaced with labels ``resource_kind`` and ``resource_name``.

Deprecated Metrics
~~~~~~~~~~~~~~~~~~


Advanced
========

Upgrade Impact
--------------

Upgrades are designed to have minimal impact on your running deployment.
Networking connectivity, policy enforcement and load balancing will remain
functional in general. The following is a list of operations that will not be
available during the upgrade:

* API-aware policy rules are enforced in user space proxies and are
  running as part of the Cilium pod. Upgrading Cilium causes the proxy to
  restart, which results in a connectivity outage and causes the connection to reset.

* Existing policy will remain effective but implementation of new policy rules
  will be postponed to after the upgrade has been completed on a particular
  node.

* Monitoring components such as ``cilium-dbg monitor`` will experience a brief
  outage while the Cilium pod is restarting. Events are queued up and read
  after the upgrade. If the number of events exceeds the event buffer size,
  events will be lost.


Migrating from kvstore-backed identities to Kubernetes CRD-backed identities
----------------------------------------------------------------------------

Beginning with Cilium 1.6, Kubernetes CRD-backed security identities can be
used for smaller clusters. Along with other changes in 1.6, this allows
kvstore-free operation if desired. It is possible to migrate identities from an
existing kvstore deployment to CRD-backed identities. This minimizes
disruptions to traffic as the update rolls out through the cluster.

Migration
~~~~~~~~~

When identities change, existing connections can be disrupted while Cilium
initializes and synchronizes with the shared identity store. The disruption
occurs when new numeric identities are used for existing pods on some instances
and others are used on others. When converting to CRD-backed identities, it is
possible to pre-allocate CRD identities so that the numeric identities match
those in the kvstore. This allows new and old Cilium instances in the rollout
to agree.

There are two ways to achieve this: you can either run a one-off ``cilium preflight migrate-identity`` script
which will perform a point-in-time copy of all identities from the kvstore to CRDs (added in Cilium 1.6), or use the "Double Write" identity
allocation mode which will have Cilium manage identities in both the kvstore and CRD at the same time for a seamless migration (added in Cilium 1.17).

Migration with the ``cilium preflight migrate-identity`` script
###############################################################

The ``cilium preflight migrate-identity`` script is a one-off tool that can be used to copy identities from the kvstore into CRDs.
It has a couple of limitations:

* If an identity is created in the kvstore after the one-off migration has been completed, it will not be copied into a CRD.
  This means that you need to perform the migration on a cluster with no identity churn.
* There is no easy way to revert back to ``--identity-allocation-mode=kvstore`` if something goes wrong after
  Cilium has been migrated to ``--identity-allocation-mode=crd``

If these limitations are not acceptable, it is recommended to use the ":ref:`Double Write <double_write_migration>`" identity allocation mode instead.

The following steps show an example of performing the migration using the ``cilium preflight migrate-identity`` script.
It is safe to re-run the command if desired. It will identify already allocated identities or ones that
cannot be migrated. Note that identity ``34815`` is migrated, ``17003`` is
already migrated, and ``11730`` has a conflict and a new ID allocated for those
labels.

The steps below assume a stable cluster with no new identities created during
the rollout. Once Cilium using CRD-backed identities is running, it may begin
allocating identities in a way that conflicts with older ones in the kvstore.

The cilium preflight manifest requires etcd support and can be built with:

.. code-block:: shell-session

    helm template cilium \
      --namespace=kube-system \
      --set preflight.enabled=true \
      --set agent=false \
      --set config.enabled=false \
      --set operator.enabled=false \
      --set etcd.enabled=true \
      --set etcd.ssl=true \
      > cilium-preflight.yaml
    kubectl create -f cilium-preflight.yaml


Example migration
~~~~~~~~~~~~~~~~~

.. code-block:: shell-session

      $ kubectl exec -n kube-system cilium-pre-flight-check-1234 -- cilium-dbg preflight migrate-identity
      INFO[0000] Setting up kvstore client
      INFO[0000] Connecting to etcd server...                  config=/var/lib/cilium/etcd-config.yml endpoints="[https://192.168.60.11:2379]" subsys=kvstore
      INFO[0000] Setting up kubernetes client
      INFO[0000] Establishing connection to apiserver          host="https://192.168.60.11:6443" subsys=k8s
      INFO[0000] Connected to apiserver                        subsys=k8s
      INFO[0000] Got lease ID 29c66c67db8870c8                 subsys=kvstore
      INFO[0000] Got lock lease ID 29c66c67db8870ca            subsys=kvstore
      INFO[0000] Successfully verified version of etcd endpoint  config=/var/lib/cilium/etcd-config.yml endpoints="[https://192.168.60.11:2379]" etcdEndpoint="https://192.168.60.11:2379" subsys=kvstore version=3.3.13
      INFO[0000] CRD (CustomResourceDefinition) is installed and up-to-date  name=CiliumNetworkPolicy/v2 subsys=k8s
      INFO[0000] Updating CRD (CustomResourceDefinition)...    name=v2.CiliumEndpoint subsys=k8s
      INFO[0001] CRD (CustomResourceDefinition) is installed and up-to-date  name=v2.CiliumEndpoint subsys=k8s
      INFO[0001] Updating CRD (CustomResourceDefinition)...    name=v2.CiliumNode subsys=k8s
      INFO[0002] CRD (CustomResourceDefinition) is installed and up-to-date  name=v2.CiliumNode subsys=k8s
      INFO[0002] Updating CRD (CustomResourceDefinition)...    name=v2.CiliumIdentity subsys=k8s
      INFO[0003] CRD (CustomResourceDefinition) is installed and up-to-date  name=v2.CiliumIdentity subsys=k8s
      INFO[0003] Listing identities in kvstore
      INFO[0003] Migrating identities to CRD
      INFO[0003] Skipped non-kubernetes labels when labelling ciliumidentity. All labels will still be used in identity determination  labels="map[]" subsys=crd-allocator
      INFO[0003] Skipped non-kubernetes labels when labelling ciliumidentity. All labels will still be used in identity determination  labels="map[]" subsys=crd-allocator
      INFO[0003] Skipped non-kubernetes labels when labelling ciliumidentity. All labels will still be used in identity determination  labels="map[]" subsys=crd-allocator
      INFO[0003] Migrated identity                             identity=34815 identityLabels="k8s:class=tiefighter;k8s:io.cilium.k8s.policy.cluster=default;k8s:io.cilium.k8s.policy.serviceaccount=default;k8s:io.kubernetes.pod.namespace=default;k8s:org=empire;"
      WARN[0003] ID is allocated to a different key in CRD. A new ID will be allocated for the this key  identityLabels="k8s:class=deathstar;k8s:io.cilium.k8s.policy.cluster=default;k8s:io.cilium.k8s.policy.serviceaccount=default;k8s:io.kubernetes.pod.namespace=default;k8s:org=empire;" oldIdentity=11730
      INFO[0003] Reusing existing global key                   key="k8s:class=deathstar;k8s:io.cilium.k8s.policy.cluster=default;k8s:io.cilium.k8s.policy.serviceaccount=default;k8s:io.kubernetes.pod.namespace=default;k8s:org=empire;" subsys=allocator
      INFO[0003] New ID allocated for key in CRD               identity=17281 identityLabels="k8s:class=deathstar;k8s:io.cilium.k8s.policy.cluster=default;k8s:io.cilium.k8s.policy.serviceaccount=default;k8s:io.kubernetes.pod.namespace=default;k8s:org=empire;" oldIdentity=11730
      INFO[0003] ID was already allocated to this key. It is already migrated  identity=17003 identityLabels="k8s:class=xwing;k8s:io.cilium.k8s.policy.cluster=default;k8s:io.cilium.k8s.policy.serviceaccount=default;k8s:io.kubernetes.pod.namespace=default;k8s:org=alliance;"

.. note::

    It is also possible to use the ``--k8s-kubeconfig-path``  and ``--kvstore-opt``
    ``cilium`` CLI options with the preflight command. The default is to derive the
    configuration as cilium-agent does.

  .. code-block:: shell-session

        cilium preflight migrate-identity --k8s-kubeconfig-path /var/lib/cilium/cilium.kubeconfig --kvstore etcd --kvstore-opt etcd.config=/var/lib/cilium/etcd-config.yml

Once the migration is complete, confirm the endpoint identities match by listing the endpoints stored in CRDs and in etcd:

.. code-block:: shell-session

      $ kubectl get ciliumendpoints -A # new CRD-backed endpoints
      $ kubectl exec -n kube-system cilium-1234 -- cilium-dbg endpoint list # existing etcd-backed endpoints

Clearing CRD identities
~~~~~~~~~~~~~~~~~~~~~~~

If a migration has gone wrong, it possible to start with a clean slate. Ensure that no Cilium instances are running with ``--identity-allocation-mode=crd`` and execute:

.. code-block:: shell-session

      $ kubectl delete ciliumid --all

.. _double_write_migration:

Migration with the "Double Write" identity allocation mode
##########################################################

.. include:: ../beta.rst

The "Double Write" Identity Allocation Mode allows Cilium to allocate identities as KVStore values *and* as CRDs at the
same time. This mode also has two versions: one where the source of truth comes from the kvstore (``--identity-allocation-mode=doublewrite-readkvstore``),
and one where the source of truth comes from CRDs (``--identity-allocation-mode=doublewrite-readcrd``).

The high-level migration plan looks as follows:

#. Starting state: Cilium is running in KVStore mode.
#. Switch Cilium to "Double Write" mode with all reads happening from the KVStore. This is almost the same as the
   pure KVStore mode with the only difference being that all identities are duplicated as CRDs but are not used.
#. Switch Cilium to "Double Write" mode with all reads happening from CRDs. This is equivalent to Cilium running in
   pure CRD mode but identities will still be updated in the KVStore to allow for the possibility of a fast rollback.
#. Switch Cilium to CRD mode. The KVStore will no longer be used and will be ready for decommission.

This will allow you to perform a gradual and seamless migration with the possibility of a fast rollback at steps two or three.

Furthermore, when the "Double Write" mode is enabled, the Operator will emit additional metrics to help monitor the
migration progress. These metrics can be used for alerting about identity inconsistencies between the KVStore and CRDs.

Note that you can also use this to migrate from CRD to KVStore mode. All operations simply need to be repeated in reverse order.

Rollout Instructions
~~~~~~~~~~~~~~~~~~~~

#. Re-deploy first the Operator and then the Agents with ``--identity-allocation-mode=doublewrite-readkvstore``.
#. Monitor the Operator metrics and logs to ensure that all identities have converged between the KVStore and CRDs. The relevant metrics emitted by the Operator are:

   * ``cilium_operator_identity_crd_total_count`` and ``cilium_operator_identity_kvstore_total_count`` report the total number of identities in CRDs and KVStore respectively.
   * ``cilium_operator_identity_crd_only_count`` and ``cilium_operator_identity_kvstore_only_count`` report the number of
     identities that are only in CRDs or only in the KVStore respectively, to help detect inconsistencies.

   In case further investigation is needed, the Operator logs will contain detailed information about the discrepancies between KVStore and CRD identities.
   Note that Garbage Collection for KVStore identities and CRD identities happens at slightly different times, so it is possible to see discrepancies in the metrics
   for certain periods of time, depending on ``--identity-gc-interval`` and ``--identity-heartbeat-timeout`` settings.
#. Once all identities have converged, re-deploy the Operator and the Agents with ``--identity-allocation-mode=doublewrite-readcrd``.
   This will cause Cilium to read identities only from CRDs, but continue to write them to the KVStore.
#. Once you are ready to decommission the KVStore, re-deploy first the Agents and then the Operator with ``--identity-allocation-mode=crd``.
   This will make Cilium read and write identities only to CRDs.
#. You can now decommission the KVStore.

.. _change_policy_default_local_cluster:

Preparing for a ``policy-default-local-cluster`` change
#######################################################

Cilium network policies used to implicitly select endpoints from all the clusters.
Cilium 1.18 introduced a new option called ``policy-default-local-cluster`` which
will be set by default in Cilium 1.19. This option restricts endpoints selection to
the local cluster by default. If you are using ClusterMesh and network policies this
will be a **breaking change** and you **need to take action** before upgrading to
Cilium 1.19.

This new option can be set in the ConfigMap or via the Helm value ``clustermesh.policyDefaultLocalCluster``.
You can set ``policy-default-local-cluster`` to ``false`` in Cilium 1.19 to keep the existing behavior,
however this option will be deprecated and eventually removed in a future release so you should plan your
migration to set ``policy-default-local-cluster`` to ``true``.

Migrating network policies in practice
~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~

The command ``cilium clustermesh inspect-policy-default-local-cluster --all-namespaces`` can help you
discover all the policies that will change as a result of changing ``policy-default-local-cluster``.
You can also replace ``--all-namespaces`` with ``-n my-namespace`` if you want to only inspect
policies from a particular namespace.

Below is an example where there is one network policy that needs to be updated:

.. code-block:: shell-session

    $ cilium clustermesh prepare-policy-default-local-cluster --all-namespaces

    ⚠️ CiliumNetworkPolicy 0/1
            ⚠️ default/allow-from-bar

    ✅ CiliumClusterWideNetworkPolicy 0/0

    ✅ NetworkPolicy 0/0


In this situation you have only one CiliumNetworkPolicy which is affected by a
``policy-default-local-cluster`` change. Let's take a look at the policy:

.. code-block:: yaml

    apiVersion: "cilium.io/v2"
    kind: CiliumNetworkPolicy
    metadata:
      name: allow-from-bar
      namespace: default
    spec:
      description: "Allow ingress traffic from bar"
      endpointSelector:
        matchLabels:
          name: foo
      ingress:
      - fromEndpoints:
        - matchLabels:
            name: bar

This network policy does not explicitly select a cluster. This means that with ``policy-default-local-cluster``
set to ``false`` it allows traffic coming from ``bar`` in any clusters connected in your ClusterMesh.
With ``policy-default-local-cluster`` set to ``true``, this policy allows traffic from ``bar`` from only
the local cluster instead.

If ``foo`` and ``bar`` are always in the same cluster, no further action is necessary.

In case you want to do this on this individual policy rather than at a global level or that
``bar`` is located on a remote cluster you can update your policy like that:

.. code-block:: yaml

    apiVersion: "cilium.io/v2"
    kind: CiliumNetworkPolicy
    metadata:
      name: allow-from-bar
      namespace: default
    spec:
      description: "Allow ingress traffic from bar"
      endpointSelector:
        matchLabels:
          name: foo
      ingress:
      - fromEndpoints:
        - matchLabels:
            name: bar
            io.cilium.k8s.policy.cluster: fixme-cluster-name

If ``bar`` is located in multiple cluster you can also use a ``matchExpressions``
selecting multiple clusters like that:

.. code-block:: yaml

    apiVersion: "cilium.io/v2"
    kind: CiliumNetworkPolicy
    metadata:
      name: allow-from-bar
      namespace: default
    spec:
      description: "Allow ingress traffic from bar"
      endpointSelector:
        matchLabels:
          name: foo
      ingress:
      - fromEndpoints:
        - matchLabels:
            name: bar
          matchExpressions:
            - key: io.cilium.k8s.policy.cluster
              operator: In
              values:
                - fixme-cluster-name-1
                - fixme-cluster-name-2

Alternatively, you can also allow traffic from ``bar`` located in every cluster and restore
the same behavior as setting ``policy-default-local-cluster`` to ``false`` but on this
individual policy:

.. code-block:: yaml

    apiVersion: "cilium.io/v2"
    kind: CiliumNetworkPolicy
    metadata:
      name: allow-from-bar
      namespace: default
    spec:
      description: "Allow ingress traffic from bar"
      endpointSelector:
        matchLabels:
          name: foo
      ingress:
      - fromEndpoints:
        - matchLabels:
            name: bar
          matchExpressions:
            - key: io.cilium.k8s.policy.cluster
              operator: Exists

.. _cnp_validation:

CNP Validation
--------------

Running the CNP Validator will make sure the policies deployed in the cluster
are valid. It is important to run this validation before an upgrade so it will
make sure Cilium has a correct behavior after upgrade. Avoiding doing this
validation might cause Cilium from updating its ``NodeStatus`` in those invalid
Network Policies as well as in the worst case scenario it might give a false
sense of security to the user if a policy is badly formatted and Cilium is not
enforcing that policy due a bad validation schema. This CNP Validator is
automatically executed as part of the pre-flight check :ref:`pre_flight`.

Start by deployment the ``cilium-pre-flight-check`` and check if the
``Deployment`` shows READY 1/1, if it does not check the pod logs.

.. code-block:: shell-session

      $ kubectl get deployment -n kube-system cilium-pre-flight-check -w
      NAME                      READY   UP-TO-DATE   AVAILABLE   AGE
      cilium-pre-flight-check   0/1     1            0           12s

      $ kubectl logs -n kube-system deployment/cilium-pre-flight-check -c cnp-validator --previous
      level=info msg="Setting up kubernetes client"
      level=info msg="Establishing connection to apiserver" host="https://172.20.0.1:443" subsys=k8s
      level=info msg="Connected to apiserver" subsys=k8s
      level=info msg="Validating CiliumNetworkPolicy 'default/cidr-rule': OK!
      level=error msg="Validating CiliumNetworkPolicy 'default/cnp-update': unexpected validation error: spec.labels: Invalid value: \"string\": spec.labels in body must be of type object: \"string\""
      level=error msg="Found invalid CiliumNetworkPolicy"

In this example, we can see the ``CiliumNetworkPolicy`` in the ``default``
namespace with the name ``cnp-update`` is not valid for the Cilium version we
are trying to upgrade. In order to fix this policy we need to edit it, we can
do this by saving the policy locally and modify it. For this example it seems
the ``.spec.labels`` has set an array of strings which is not correct as per
the official schema.

.. code-block:: shell-session

      $ kubectl get cnp -n default cnp-update -o yaml > cnp-bad.yaml
      $ cat cnp-bad.yaml
        apiVersion: cilium.io/v2
        kind: CiliumNetworkPolicy
        [...]
        spec:
          endpointSelector:
            matchLabels:
              id: app1
          ingress:
          - fromEndpoints:
            - matchLabels:
                id: app2
            toPorts:
            - ports:
              - port: "80"
                protocol: TCP
          labels:
          - custom=true
        [...]

To fix this policy we need to set the ``.spec.labels`` with the right format and
commit these changes into Kubernetes.

.. code-block:: shell-session

      $ cat cnp-bad.yaml
        apiVersion: cilium.io/v2
        kind: CiliumNetworkPolicy
        [...]
        spec:
          endpointSelector:
            matchLabels:
              id: app1
          ingress:
          - fromEndpoints:
            - matchLabels:
                id: app2
            toPorts:
            - ports:
              - port: "80"
                protocol: TCP
          labels:
          - key: "custom"
            value: "true"
        [...]
      $
      $ kubectl apply -f ./cnp-bad.yaml

After applying the fixed policy we can delete the pod that was validating the
policies so that Kubernetes creates a new pod immediately to verify if the fixed
policies are now valid.

.. code-block:: shell-session

      $ kubectl delete pod -n kube-system -l k8s-app=cilium-pre-flight-check-deployment
      pod "cilium-pre-flight-check-86dfb69668-ngbql" deleted
      $ kubectl get deployment -n kube-system cilium-pre-flight-check
      NAME                      READY   UP-TO-DATE   AVAILABLE   AGE
      cilium-pre-flight-check   1/1     1            1           55m
      $ kubectl logs -n kube-system deployment/cilium-pre-flight-check -c cnp-validator
      level=info msg="Setting up kubernetes client"
      level=info msg="Establishing connection to apiserver" host="https://172.20.0.1:443" subsys=k8s
      level=info msg="Connected to apiserver" subsys=k8s
      level=info msg="Validating CiliumNetworkPolicy 'default/cidr-rule': OK!
      level=info msg="Validating CiliumNetworkPolicy 'default/cnp-update': OK!
      level=info msg="All CCNPs and CNPs valid!"

Once they are valid you can continue with the upgrade process. :ref:`cleanup_preflight_check`
