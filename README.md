# Percona Operator for MySQL based on Percona XtraDB Cluster Provider

This directory contains an implementation of a Percona Operator for MySQL based on Percona XtraDB Cluster (PXC) provider.

## Installation

The provider chart is published as an OCI artifact to the GitHub Container
Registry. It bundles the Percona Operator for MySQL (PXC) as a subchart, so a
single install brings up both the provider and the operator.

```bash
helm install provider-percona-xtradb-cluster \
  oci://ghcr.io/openeverest/charts/provider-percona-xtradb-cluster \
  --version 0.1.0 \
  --create-namespace
```

Upgrade to a newer chart version:

```bash
helm upgrade provider-percona-xtradb-cluster \
  oci://ghcr.io/openeverest/charts/provider-percona-xtradb-cluster \
  --version 0.1.0
```

Uninstall:

```bash
helm uninstall provider-percona-xtradb-cluster
```

> Browse available versions on the
> [chart package page](https://github.com/openeverest/provider-percona-xtradb-cluster/pkgs/container/charts%2Fprovider-percona-xtradb-cluster).

## 🚀 Quick Start

### Prerequisites

1. A Kubernetes cluster:

```
make k3d-cluster-up
```

2. Generate Provider CR manifests (if changed):

```bash
make generate
```

3. Install CRDs:

    make install-crds

### Run the Provider

```bash
make run
```

### Create a Test

```bash
kubectl apply -f examples/instance-simple.yaml
```

Watch the provider logs and check the PXC resource:

```bash
kubectl get pxc
kubectl get instance
```

## 🧪 Running Integration Tests

The `test/integration/` directory contains kuttl tests that verify the provider's behavior.

### Prerequisites for Tests

1. SDK CRDs installed (see Quick Start above)
2. Provider running in the background:
```bash
make run
```

### Running the Tests

```bash
# From the examples directory:
make test-integration

# Or run directly:
cd examples
. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl.yaml
```

**Note:** The tests assume the provider is already running and will create/update/delete Instance resources to verify correct behavior.
