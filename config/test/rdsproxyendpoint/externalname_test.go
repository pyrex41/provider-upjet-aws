// SPDX-FileCopyrightText: 2026 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

// package rdsproxyendpoint tests the registered external name configuration of
// aws_db_proxy_endpoint in the cluster-scoped and namespaced providers.
package rdsproxyendpoint

import (
	"context"
	"slices"
	"testing"

	ujconfig "github.com/crossplane/upjet/v2/pkg/config"
	"github.com/hashicorp/terraform-provider-aws/xpprovider"

	"github.com/upbound/provider-aws/v2/config"
)

const resourceName = "aws_db_proxy_endpoint"

// externalNames returns the aws_db_proxy_endpoint external name configuration
// registered in each provider scope.
func externalNames(t *testing.T) map[string]ujconfig.ExternalName {
	t.Helper()
	fwProvider, sdkProvider, err := xpprovider.GetProvider(t.Context())
	if err != nil {
		t.Fatalf("xpprovider.GetProvider: %s", err)
	}
	cluster, err := config.GetProvider(t.Context(), fwProvider, sdkProvider, false, false)
	if err != nil {
		t.Fatalf("GetProvider: %s", err)
	}
	namespaced, err := config.GetProviderNamespaced(t.Context(), fwProvider, sdkProvider, false, false)
	if err != nil {
		t.Fatalf("GetProviderNamespaced: %s", err)
	}
	out := map[string]ujconfig.ExternalName{}
	for scope, p := range map[string]*ujconfig.Provider{"cluster": cluster, "namespaced": namespaced} {
		r, ok := p.Resources[resourceName]
		if !ok {
			t.Fatalf("%s provider: %s is not registered", scope, resourceName)
		}
		out[scope] = r.ExternalName
	}
	return out
}

// terraformID builds the ID the way the Terraform client does for an
// observation: the forProvider parameters, with the external name injected as
// the identifier argument, rendered through GetIDFn.
func terraformID(t *testing.T, e ujconfig.ExternalName, externalName, proxyName string) (string, map[string]any) {
	t.Helper()
	params := map[string]any{
		"db_proxy_name": proxyName,
		"region":        "us-east-2",
		"target_role":   "READ_ONLY",
	}
	e.SetIdentifierArgumentFn(params, externalName)
	id, err := e.GetIDFn(context.Background(), externalName, params, map[string]any{})
	if err != nil {
		t.Fatalf("GetIDFn(%q): %s", externalName, err)
	}
	return id, params
}

func externalNameFromID(t *testing.T, e ujconfig.ExternalName, id string) string {
	t.Helper()
	got, err := e.GetExternalNameFn(map[string]any{"id": id})
	if err != nil {
		t.Fatalf("GetExternalNameFn(%q): %s", id, err)
	}
	return got
}

// TestProxyEndpointExternalName checks the mapping against the Terraform
// resource ID, which internal/service/rds/proxy_endpoint.go builds as
// DBPROXYNAME/DBPROXYENDPOINTNAME (proxyEndpointCreateResourceID) and parses
// with SplitN(id, "/", 2) (proxyEndpointParseResourceID).
func TestProxyEndpointExternalName(t *testing.T) {
	const (
		proxy    = "rds-proxy-prod"
		endpoint = "rds-proxy-prod-reader"
	)
	for scope, e := range externalNames(t) {
		t.Run(scope, func(t *testing.T) {
			t.Run("IdentifierFields", func(t *testing.T) {
				for _, f := range []string{"db_proxy_name", "region"} {
					if !slices.Contains(e.IdentifierFields, f) {
						t.Errorf("IdentifierFields = %v, missing %q", e.IdentifierFields, f)
					}
				}
				if !slices.Contains(e.OmittedFields, "db_proxy_endpoint_name") {
					t.Errorf("OmittedFields = %v, missing db_proxy_endpoint_name", e.OmittedFields)
				}
			})

			t.Run("ForwardIDWithDifferentNames", func(t *testing.T) {
				id, params := terraformID(t, e, endpoint, proxy)
				if want := proxy + "/" + endpoint; id != want {
					t.Errorf("GetIDFn = %q, want %q", id, want)
				}
				if got := params["db_proxy_endpoint_name"]; got != endpoint {
					t.Errorf("injected db_proxy_endpoint_name = %v, want %q", got, endpoint)
				}
				if got := params["db_proxy_name"]; got != proxy {
					t.Errorf("db_proxy_name = %v, want %q (must not be overwritten)", got, proxy)
				}
			})

			t.Run("ReverseExternalNameFromTerraformID", func(t *testing.T) {
				// The ID recorded in status.atProvider.id in production.
				if got := externalNameFromID(t, e, proxy+"/"+endpoint); got != endpoint {
					t.Errorf("GetExternalNameFn(%q) = %q, want %q", proxy+"/"+endpoint, got, endpoint)
				}
			})

			t.Run("RoundTripWithDifferentNames", func(t *testing.T) {
				id, _ := terraformID(t, e, endpoint, proxy)
				if got := externalNameFromID(t, e, id); got != endpoint {
					t.Errorf("round trip: GetIDFn = %q, GetExternalNameFn = %q, want %q", id, got, endpoint)
				}
			})

			t.Run("SameNames", func(t *testing.T) {
				// Same-name tests alone cannot detect a reversed template.
				id, _ := terraformID(t, e, "db-proxy", "db-proxy")
				if id != "db-proxy/db-proxy" {
					t.Errorf("GetIDFn = %q, want %q", id, "db-proxy/db-proxy")
				}
				if got := externalNameFromID(t, e, id); got != "db-proxy" {
					t.Errorf("GetExternalNameFn(%q) = %q, want %q", id, got, "db-proxy")
				}
			})

			t.Run("CorruptAnnotationIsNotRepaired", func(t *testing.T) {
				// The production annotation was overwritten with the proxy
				// name. Nothing in the provider's inputs distinguishes it from
				// a real endpoint with the proxy's name, so the provider must
				// look up proxy/proxy (reported as missing) and keep the
				// annotation, never infer the endpoint name. The annotation
				// needs an explicit correction.
				id, params := terraformID(t, e, proxy, proxy)
				if want := proxy + "/" + proxy; id != want {
					t.Errorf("GetIDFn = %q, want %q", id, want)
				}
				if id == proxy+"/"+endpoint {
					t.Errorf("GetIDFn inferred the endpoint name from a corrupt annotation: %q", id)
				}
				if got := params["db_proxy_endpoint_name"]; got != proxy {
					t.Errorf("injected db_proxy_endpoint_name = %v, want %q", got, proxy)
				}
				if got := externalNameFromID(t, e, id); got != proxy {
					t.Errorf("GetExternalNameFn(%q) = %q, want %q", id, got, proxy)
				}
			})
		})
	}
}
