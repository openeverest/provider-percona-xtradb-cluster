package provider

// Run `make manifests` to regenerate config/rbac/role.yaml from these markers.
// This file contains kubebuilder RBAC markers for controller-gen.
// See: https://book.kubebuilder.io/reference/markers/rbac

// Base RBAC (required by all providers):
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core.openeverest.io,resources=instances/finalizers,verbs=update
// +kubebuilder:rbac:groups=core.openeverest.io,resources=providers,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Startup gate (provider-runtime holds a new build until the installed
// OpenEverest supports its contract version, then records that version on
// this provider's Provider):
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,resourceNames=providers.core.openeverest.io,verbs=get
// +kubebuilder:rbac:groups=core.openeverest.io,resources=providers/status,resourceNames=percona-xtradb-cluster,verbs=patch

// =============================================================================
// PROVIDER-SPECIFIC RBAC — Percona XtraDB Cluster operator resources.
// =============================================================================
// Allow managing connection and PMM Secrets in tenant namespaces.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// Allow the runtime to report on the component pods in the Instance status.
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// Allow tracking rollouts: an Instance has converged only once the cluster's
// StatefulSets run their latest pod template.
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=list;watch
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusters/status,verbs=get
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterbackups/status,verbs=get
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterrestores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterrestores/status,verbs=get
// +kubebuilder:rbac:groups=pxc.percona.com,resources=perconaxtradbclusterrestores/finalizers,verbs=update
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backupclasses;backupstorageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backupclasses/status,verbs=patch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backupstorages,verbs=get;list;watch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=backups/finalizers,verbs=update
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=backup.openeverest.io,resources=restores/finalizers,verbs=update
// Allow the runtime to resolve monitoring bindings, report on them and stamp
// the cluster identity on monitoring sources.
// +kubebuilder:rbac:groups=monitoring.openeverest.io,resources=monitoringclasses;monitoringdestinations;monitoringbindings,verbs=get;list;watch
// +kubebuilder:rbac:groups=monitoring.openeverest.io,resources=monitoringbindings/status,verbs=patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get
// Allow reissuing the cluster's certificates when its additional names change:
// the operator never updates the cert-manager Certificates it creates.
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;patch
