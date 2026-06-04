# Provider Definition

This directory contains all the files a developer edits when creating or
maintaining an OpenEverest provider. Everything related to the provider's
**identity**, **versions**, **topologies**, **UI**, and **custom schemas** lives
here.

## Directory layout

```
definition/
├── provider.yaml                    # Provider name + component→type mapping
├── versions.yaml                    # Component types and their version/image catalog
├── types.go                         # Shared types (TopologyType, GlobalConfig)
├── components/
│   └── types.go                     # Component custom spec types
├── topologies/
│   └── cluster/
│       ├── topology.yaml            # Topology config + UI schema (co-located)
│       └── types.go                 # ClusterTopologyConfig
└── backupclasses/
    └── <name>/
        ├── class.yaml              # BackupClass metadata, limits, schema refs
        └── types.go                # Go types for backup/restore/PITR custom config
```

## What each file does

| File | Purpose | When to edit |
|------|---------|--------------|
| `provider.yaml` | Names the provider and maps logical component names (e.g. `engine`, `monitoring`) to component types (e.g. `pxc`, `pmm`). | When adding/removing a component. |
| `versions.yaml` | Lists available versions and container images for each component type. | When a new upstream release is available. |
| `types.go` | Shared Go types used across the provider (e.g. `TopologyType`, `GlobalConfig`). | When adding provider-wide types. |
| `components/types.go` | Go structs for component custom specs, one per component type. | When a component type needs custom configuration. |
| `topologies/<name>/topology.yaml` | Defines a deployment topology (which components, defaults) **and** its UI rendering hints — co-located for clarity. | When adding a topology or changing its UI. |
| `topologies/<name>/types.go` | Go struct for topology-specific custom config. Referenced by `topology.yaml` via `configSchema`. | When a topology needs custom configuration fields. |
| `backupclasses/<name>/class.yaml` | BackupClass metadata: `executionMode`, `supportedProviders`, `providerManaged.limits` (caps the runtime enforces against Instance.spec.backup), `providerManaged.pitrConfigSchema`, and `config.openAPIV3Schema` / `restoreConfig.openAPIV3Schema` Go type references. | When adding a new backup class or changing its capabilities. |
| `backupclasses/<name>/ui.yaml` | Free-form UI schema grouped by modal: `backup`, `pitr`, `restore`. Inlined verbatim under `spec.uiSchema` on the generated BackupClass. | When the form a user fills in to configure backups changes. |
| `backupclasses/<name>/types.go` | Go structs whose OpenAPI v3 representations are inlined into `config.openAPIV3Schema`, `restoreConfig.openAPIV3Schema`, and `providerManaged.pitrConfigSchema`. | When the backup/restore/PITR config shape changes. |

## How it all fits together

```
definition/ files
       │
       ▼
provider-sdk generate  ──▶  charts/<name>/generated/provider-spec.yaml
       │                     charts/<name>/generated/backupclasses/<name>.yaml
       ▼
Helm chart template    ──▶  .Files.Get "generated/provider-spec.yaml"
                             .Files.Glob "generated/backupclasses/*.yaml"
```

Run `make generate` to regenerate all code.

## Adding a new topology

1. Create a directory `definition/topologies/<name>/` with a `topology.yaml`
   containing `config:` and `ui:` sections (copy an existing topology as a
   starting point).
2. Create a `types.go` in the same directory with a Go struct for custom config.
   Reference it via `configSchema` in the topology YAML.
3. Run `make gen` to regenerate.
