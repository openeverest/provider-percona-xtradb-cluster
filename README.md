# Percona Operator for MySQL based on Percona XtraDB Cluster Provider

This directory contains a implementation of a Percona Operator for MySQL based on Percona XtraDB Cluster (PXC) provider.

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
```bash
make install
```

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
