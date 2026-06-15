// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// package main implements the provider for Percona Operator for MySQL based on Percona XtraDB Cluster
package main

import (
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openeverest/openeverest/v2/provider-runtime/reconciler"

	"github.com/openeverest/provider-percona-xtradb-cluster/internal/provider"
)

// main is the entry point for the provider.
func main() {
	l := ctrl.Log.WithName("setup")
	ctx := ctrl.SetupSignalHandler()

	provider := provider.NewPXCProviderInterface()

	r, err := reconciler.New(ctx, provider,
		// Enable HTTP server for validation endpoint
		reconciler.WithServer(reconciler.ServerConfig{
			Port:           8082,
			ValidationPath: "/validate",
		}),
	)

	if err != nil {
		l.Error(err, "unable to create reconciler")
		os.Exit(1)
	}

	// Inject the manager's client so watch handlers can
	// list Instance objects that reference MonitoringConfig.
	// TODO: change the way manager is configured so injection is not necessary.
	provider.SetClient(r.GetManager().GetClient())

	if err := r.Start(ctx); err != nil {
		l.Error(err, "unable to start reconciler")
		os.Exit(1)
	}
}
