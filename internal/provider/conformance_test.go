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

package provider

import (
	"testing"

	"github.com/openeverest/openeverest/v2/provider-runtime/conformance"
)

var unverifiableFields = map[string]string{
	"spec.components.proxy.type": "proxy.type is a closed enum; an invalid value is rejected by validation rather than reaching the engine CR",
	// mode is a closed enum reshaped into pxc.Spec.TLS.Enabled (a bool) plus,
	// for requireTLS, a require_secure_transport directive added to the
	// engine's my.cnf: an arbitrary probe string is neither "disabled" nor
	// "requireTLS", so it takes the same path as the default and leaves
	// nothing distinctive to observe (TestConfigureTLS covers the real
	// values).
	"spec.parameters.tls.mode": "closed enum reshaped into TLS.Enabled and an engine configuration directive; see TestConfigureTLS",
	// kind only takes effect together with issuer.name (the harness probes
	// one field at a time).
	"spec.parameters.tls.issuer.kind": "applies only with issuer.name set; see TestConfigureTLS",

}

func TestUISchemaIsReconciled(t *testing.T) {
	conformance.UISchemaIsReconciled(t, conformance.Config{
		Provider:     NewPXCProviderInterface(),
		Unverifiable: unverifiableFields,
	})
}

func TestSupportedFieldsAreReconciled(t *testing.T) {
	conformance.SupportedFieldsAreReconciled(t, conformance.Config{
		Provider:     NewPXCProviderInterface(),
		Unverifiable: unverifiableFields,
	})
}
