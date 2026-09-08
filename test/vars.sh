#!/bin/bash

## ===== General environment variables for the Percona Operator tests =====
export OPERATOR_ROOT_PATH=${OPERATOR_ROOT_PATH:-${PWD}}
echo "OPERATOR_ROOT_PATH=${OPERATOR_ROOT_PATH}"

## ======= Upstream DB operators params for testing ===============

# Recommended PXC operator version for tests.
export PXC_OPERATOR_VERSION=${PXC_OPERATOR_VERSION:-"1.20.0"}
echo "PXC_OPERATOR_VERSION=${PXC_OPERATOR_VERSION}"

# Recommended PXC engine version for tests.
export PXC_DB_ENGINE_VERSION=${PXC_DB_ENGINE_VERSION:-"8.4.8"}
echo "PXC_DB_ENGINE_VERSION=${PXC_DB_ENGINE_VERSION}"

# Previous versions for upgrade tests.
export PREVIOUS_PXC_DB_ENGINE_VERSION=${PREVIOUS_PXC_DB_ENGINE_VERSION:-"8.0.45"}
echo "PREVIOUS_PXC_DB_ENGINE_VERSION=${PREVIOUS_PXC_DB_ENGINE_VERSION}"

export PREVIOUS_PXC_OPERATOR_VERSION=${PREVIOUS_PXC_OPERATOR_VERSION:-"1.19.1"}
echo "PREVIOUS_PXC_OPERATOR_VERSION=${PREVIOUS_PXC_OPERATOR_VERSION}"

## ============== K3D cluster configuration ===================
# export KUBECONFIG="${KUBECONFIG:-${OPERATOR_ROOT_PATH}/test/kubeconfig}"
# echo "KUBECONFIG=${KUBECONFIG}"

