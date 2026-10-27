// SPDX-FileCopyrightText: 2026 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

// package rdsproxyendpoint tests the registered external name configuration of
// aws_db_proxy_endpoint in the cluster-scoped and namespaced providers.
//
// TestProxyEndpointExternalName, TestProxyEndpointExternalNameProperty and
// TestProxyEndpointColdStart fail on the reversed v2.7.0 template and pass on
// the corrected one. TestProxyEndpointExternalNameInvariants passes on both:
// it records behaviour the correction must not change.
package rdsproxyendpoint

import (
	"context"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	xpresource "github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	ujconfig "github.com/crossplane/upjet/v2/pkg/config"
	tjcontroller "github.com/crossplane/upjet/v2/pkg/controller"
	ujresource "github.com/crossplane/upjet/v2/pkg/resource"
	"github.com/crossplane/upjet/v2/pkg/terraform"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-provider-aws/xpprovider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	clusterv1beta1 "github.com/upbound/provider-aws/v2/apis/cluster/rds/v1beta1"
	namespacedv1beta1 "github.com/upbound/provider-aws/v2/apis/namespaced/rds/v1beta1"
	"github.com/upbound/provider-aws/v2/config"
)

const (
	resourceName = "aws_db_proxy_endpoint"
	proxy        = "rds-proxy-prod"
	endpoint     = "rds-proxy-prod-reader"
)

var (
	providersOnce sync.Once
	providers     map[string]*ujconfig.Provider
	providersErr  error
)

// registered returns the aws_db_proxy_endpoint resource configuration
// registered in each provider scope.
func registered(t *testing.T) map[string]*ujconfig.Resource {
	t.Helper()
	providersOnce.Do(func() {
		ctx := context.Background()
		fwProvider, sdkProvider, err := xpprovider.GetProvider(ctx)
		if err != nil {
			providersErr = err
			return
		}
		cluster, err := config.GetProvider(ctx, fwProvider, sdkProvider, false, false)
		if err != nil {
			providersErr = err
			return
		}
		namespaced, err := config.GetProviderNamespaced(ctx, fwProvider, sdkProvider, false, false)
		if err != nil {
			providersErr = err
			return
		}
		providers = map[string]*ujconfig.Provider{"cluster": cluster, "namespaced": namespaced}
	})
	if providersErr != nil {
		t.Fatalf("building providers: %s", providersErr)
	}
	out := map[string]*ujconfig.Resource{}
	for scope, p := range providers {
		r, ok := p.Resources[resourceName]
		if !ok {
			t.Fatalf("%s provider: %s is not registered", scope, resourceName)
		}
		out[scope] = r
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
// with SplitN(id, "/", 2) (proxyEndpointParseResourceID). Every subtest fails
// on v2.7.0.
func TestProxyEndpointExternalName(t *testing.T) {
	for scope, r := range registered(t) {
		e := r.ExternalName
		t.Run(scope, func(t *testing.T) {
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
		})
	}
}

// randomName returns an RDS-style identifier: a letter, then letters, digits
// and single hyphens, at most 63 characters, not ending in a hyphen.
func randomName(r *rand.Rand) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const rest = letters + "0123456789-"
	n := 1 + r.IntN(63)
	b := []byte{letters[r.IntN(len(letters))]}
	for len(b) < n {
		c := rest[r.IntN(len(rest))]
		if c == '-' && (b[len(b)-1] == '-' || len(b) == n-1) {
			continue
		}
		b = append(b, c)
	}
	return string(b)
}

// TestProxyEndpointExternalNameProperty checks the forward ID and the round
// trip through the Terraform ID for random different proxy and endpoint
// names. It fails on v2.7.0 at the first pair.
func TestProxyEndpointExternalNameProperty(t *testing.T) {
	for scope, res := range registered(t) {
		e := res.ExternalName
		t.Run(scope, func(t *testing.T) {
			r := rand.New(rand.NewPCG(3014, 2195))
			for i := 0; i < 5000; i++ {
				p, n := randomName(r), randomName(r)
				if p == n {
					continue
				}
				id, _ := terraformID(t, e, n, p)
				if id != p+"/"+n {
					t.Fatalf("GetIDFn(%q, db_proxy_name=%q) = %q, want %q", n, p, id, p+"/"+n)
				}
				if got := externalNameFromID(t, e, id); got != n {
					t.Fatalf("GetExternalNameFn(GetIDFn(%q, db_proxy_name=%q)) = %q", n, p, got)
				}
			}
		})
	}
}

// TestProxyEndpointExternalNameInvariants passes on v2.7.0 as well. The
// reversed template is self-consistent, so these cannot detect the bug; they
// pin behaviour the correction must keep.
func TestProxyEndpointExternalNameInvariants(t *testing.T) {
	for scope, r := range registered(t) {
		e := r.ExternalName
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

			t.Run("RoundTripWithDifferentNames", func(t *testing.T) {
				id, _ := terraformID(t, e, endpoint, proxy)
				if got := externalNameFromID(t, e, id); got != endpoint {
					t.Errorf("round trip: GetIDFn = %q, GetExternalNameFn = %q, want %q", id, got, endpoint)
				}
			})

			t.Run("SameNames", func(t *testing.T) {
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
				// a real endpoint with the proxy's name, so the provider looks
				// up proxy/proxy and keeps the annotation; it never infers the
				// endpoint name. The annotation needs an explicit correction.
				id, params := terraformID(t, e, proxy, proxy)
				if want := proxy + "/" + proxy; id != want {
					t.Errorf("GetIDFn = %q, want %q", id, want)
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

// fakeRDS stands in for the DB proxy endpoint API behind the Terraform
// resource. It keys endpoints by (proxy, endpoint) and records the ID of every
// Read and the names of every Create.
type fakeRDS struct {
	mu        sync.Mutex
	endpoints map[[2]string]bool
	reads     []string
	creates   [][2]string
}

func (f *fakeRDS) read(_ context.Context, d *schema.ResourceData, _ any) diag.Diagnostics {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads = append(f.reads, d.Id())
	parts := strings.SplitN(d.Id(), "/", 2)
	if len(parts) != 2 || !f.endpoints[[2]string{parts[0], parts[1]}] {
		d.SetId("")
		return nil
	}
	for k, v := range map[string]any{
		"db_proxy_name":          parts[0],
		"db_proxy_endpoint_name": parts[1],
		"arn":                    "arn:aws:rds:us-east-2:660035875768:db-proxy-endpoint:prx-endpoint-0ea39856dac3afbb4",
		"endpoint":               parts[1] + ".endpoint.proxy-clcyykk44ku8.us-east-2.rds.amazonaws.com",
		"target_role":            "READ_ONLY",
		"vpc_subnet_ids":         []string{"subnet-a", "subnet-b"},
		"region":                 "us-east-2",
	} {
		if err := d.Set(k, v); err != nil {
			return diag.FromErr(err)
		}
	}
	return nil
}

func (f *fakeRDS) create(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	p, n := d.Get("db_proxy_name").(string), d.Get("db_proxy_endpoint_name").(string)
	f.mu.Lock()
	f.creates = append(f.creates, [2]string{p, n})
	f.endpoints[[2]string{p, n}] = true
	f.mu.Unlock()
	d.SetId(p + "/" + n) // proxyEndpointCreateResourceID
	return f.read(ctx, d, meta)
}

func (f *fakeRDS) lastRead() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reads) == 0 {
		return ""
	}
	return f.reads[len(f.reads)-1]
}

// withFakeAPI returns a copy of the registered configuration whose Terraform
// resource calls fake instead of AWS. Everything else, including the
// external-name configuration, schema and conversions, is the registered one.
// CustomizeDiff is dropped because the AWS tagging hook needs a real client.
func withFakeAPI(r *ujconfig.Resource, f *fakeRDS) *ujconfig.Resource {
	c := *r
	tr := *r.TerraformResource
	tr.Read, tr.ReadContext, tr.ReadWithoutTimeout = nil, nil, f.read
	tr.Create, tr.CreateContext, tr.CreateWithoutTimeout = nil, nil, f.create
	tr.CustomizeDiff = nil
	c.TerraformResource = &tr
	return &c
}

type noopCallbacks struct{}

func (noopCallbacks) Create(types.NamespacedName, bool) terraform.CallbackFn {
	return func(error, context.Context) error { return nil }
}

func (noopCallbacks) Update(types.NamespacedName, bool) terraform.CallbackFn {
	return func(error, context.Context) error { return nil }
}

func (noopCallbacks) Destroy(types.NamespacedName, bool) terraform.CallbackFn {
	return func(error, context.Context) error { return nil }
}

// newManaged returns a ProxyEndpoint in the given scope with the given
// external name and management policies.
func newManaged(scope, externalName string, policies xpv2.ManagementPolicies) xpresource.Managed {
	om := metav1.ObjectMeta{Name: endpoint, UID: types.UID(scope + "-" + externalName)}
	var mg xpresource.Managed
	switch scope {
	case "cluster":
		o := &clusterv1beta1.ProxyEndpoint{ObjectMeta: om}
		o.Spec.ForProvider.DBProxyName = ptr(proxy)
		o.Spec.ForProvider.Region = ptr("us-east-2")
		o.Spec.ForProvider.TargetRole = ptr("READ_ONLY")
		o.Spec.ForProvider.VPCSubnetIds = []*string{ptr("subnet-a"), ptr("subnet-b")}
		o.SetGroupVersionKind(clusterv1beta1.ProxyEndpoint_GroupVersionKind)
		mg = o
	default:
		om.Namespace = "crossplane-system"
		o := &namespacedv1beta1.ProxyEndpoint{ObjectMeta: om}
		o.Spec.ForProvider.DBProxyName = ptr(proxy)
		o.Spec.ForProvider.Region = ptr("us-east-2")
		o.Spec.ForProvider.TargetRole = ptr("READ_ONLY")
		o.Spec.ForProvider.VPCSubnetIds = []*string{ptr("subnet-a"), ptr("subnet-b")}
		o.SetGroupVersionKind(namespacedv1beta1.ProxyEndpoint_GroupVersionKind)
		mg = o
	}
	meta.SetExternalName(mg, externalName)
	mg.(ujresource.Terraformed).SetManagementPolicies(policies)
	return mg
}

func ptr(s string) *string { return &s }

// coldObserve connects with a new, empty operation tracker store, as after a
// provider restart, through the connector the generated controller uses, and
// observes once.
func coldObserve(t *testing.T, cfg *ujconfig.Resource, mg xpresource.Managed) managedObservation {
	t.Helper()
	return observe(t, tjcontroller.NewOperationStore(logging.NewNopLogger()), cfg, mg)
}

type managedObservation struct {
	exists, lateInitialized bool
}

func connector(ots *tjcontroller.OperationTrackerStore, cfg *ujconfig.Resource) *tjcontroller.TerraformPluginSDKAsyncConnector {
	kube := fake.NewClientBuilder().Build()
	setup := func(context.Context, client.Client, xpresource.Managed) (terraform.Setup, error) {
		return terraform.Setup{}, nil
	}
	return tjcontroller.NewTerraformPluginSDKAsyncConnector(kube, ots, setup, cfg,
		tjcontroller.WithTerraformPluginSDKAsyncLogger(logging.NewNopLogger()),
		tjcontroller.WithTerraformPluginSDKAsyncCallbackProvider(noopCallbacks{}),
		tjcontroller.WithTerraformPluginSDKAsyncManagementPolicies(true),
	)
}

func observe(t *testing.T, ots *tjcontroller.OperationTrackerStore, cfg *ujconfig.Resource, mg xpresource.Managed) managedObservation {
	t.Helper()
	ctx := context.Background()
	ext, err := connector(ots, cfg).Connect(ctx, mg)
	if err != nil {
		t.Fatalf("Connect: %s", err)
	}
	o, err := ext.Observe(ctx, mg)
	if err != nil {
		t.Fatalf("Observe: %s", err)
	}
	return managedObservation{exists: o.ResourceExists, lateInitialized: o.ResourceLateInitialized}
}

func atProviderID(t *testing.T, mg xpresource.Managed) string {
	t.Helper()
	obs, err := mg.(ujresource.Terraformed).GetObservation()
	if err != nil {
		t.Fatalf("GetObservation: %s", err)
	}
	id, _ := obs["id"].(string)
	return id
}

// TestProxyEndpointColdStart drives the registered configuration through the
// generated controller's connector (upjet's Terraform plugin SDK async
// connector) with an empty operation tracker store, so the Terraform state is
// rebuilt from the managed resource as after a provider restart. The Terraform
// resource's Read and Create are replaced by fakeRDS; everything else is real.
func TestProxyEndpointColdStart(t *testing.T) {
	observeOnly := xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
	all := xpv2.ManagementPolicies{xpv2.ManagementActionAll}
	for scope, r := range registered(t) {
		t.Run(scope, func(t *testing.T) {
			t.Run("EndpointAnnotationFindsEndpoint", func(t *testing.T) {
				api := &fakeRDS{endpoints: map[[2]string]bool{{proxy, endpoint}: true}}
				mg := newManaged(scope, endpoint, observeOnly)
				o := coldObserve(t, withFakeAPI(r, api), mg)
				if got, want := api.lastRead(), proxy+"/"+endpoint; got != want {
					t.Errorf("Read ID = %q, want %q", got, want)
				}
				if !o.exists {
					t.Errorf("ResourceExists = false, want true")
				}
				if o.lateInitialized {
					t.Errorf("ResourceLateInitialized = true, want false")
				}
				if got := atProviderID(t, mg); got != proxy+"/"+endpoint {
					t.Errorf("status.atProvider.id = %q, want %q", got, proxy+"/"+endpoint)
				}
				if got := meta.GetExternalName(mg); got != endpoint {
					t.Errorf("external name = %q, want %q (unchanged)", got, endpoint)
				}
			})

			t.Run("CreateThenColdObserveKeepsEndpointName", func(t *testing.T) {
				api := &fakeRDS{endpoints: map[[2]string]bool{}}
				cfg := withFakeAPI(r, api)
				mg := newManaged(scope, endpoint, all)
				ots := tjcontroller.NewOperationStore(logging.NewNopLogger())
				// One reconcile: Connect, Observe (not found), Create.
				ctx := context.Background()
				ext, err := connector(ots, cfg).Connect(ctx, mg)
				if err != nil {
					t.Fatalf("Connect: %s", err)
				}
				if o, err := ext.Observe(ctx, mg); err != nil || o.ResourceExists {
					t.Fatalf("Observe before Create: exists=%v, err=%v", o.ResourceExists, err)
				}
				if _, err := ext.Create(ctx, mg); err != nil {
					t.Fatalf("Create: %s", err)
				}
				tracker := ots.Tracker(mg.(ujresource.Terraformed))
				deadline := time.Now().Add(30 * time.Second)
				for tracker.LastOperation.IsRunning() {
					if time.Now().After(deadline) {
						t.Fatalf("async Create still running after 30s")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := tracker.LastOperation.Error(); err != nil {
					t.Fatalf("async Create: %s", err)
				}
				if want := [][2]string{{proxy, endpoint}}; !slices.Equal(api.creates, want) {
					t.Fatalf("CreateDBProxyEndpoint calls = %v, want %v", api.creates, want)
				}
				// The warm Observe after Create is where the reversed template
				// rewrote the annotation to the proxy name.
				if o := observe(t, ots, cfg, mg); !o.exists {
					t.Fatalf("warm Observe after Create: ResourceExists = false")
				}
				if got := meta.GetExternalName(mg); got != endpoint {
					t.Fatalf("external name after Create = %q, want %q", got, endpoint)
				}
				mg.(ujresource.Terraformed).SetManagementPolicies(observeOnly)
				o := coldObserve(t, cfg, mg)
				if got, want := api.lastRead(), proxy+"/"+endpoint; got != want {
					t.Errorf("cold Read ID = %q, want %q", got, want)
				}
				if !o.exists {
					t.Errorf("cold Observe: ResourceExists = false, want true")
				}
				if got := meta.GetExternalName(mg); got != endpoint {
					t.Errorf("external name after cold Observe = %q, want %q", got, endpoint)
				}
				if len(api.creates) != 1 {
					t.Errorf("CreateDBProxyEndpoint calls = %v, want exactly one", api.creates)
				}
			})
		})
	}
}

// TestProxyEndpointColdStartInvariants passes on v2.7.0 as well.
func TestProxyEndpointColdStartInvariants(t *testing.T) {
	observeOnly := xpv2.ManagementPolicies{xpv2.ManagementActionObserve}
	for scope, r := range registered(t) {
		t.Run(scope, func(t *testing.T) {
			t.Run("ProxyNameAnnotationIsNotFoundAndKept", func(t *testing.T) {
				// The corrupt production annotation. Under Observe the provider
				// reports the endpoint missing and leaves the annotation alone.
				api := &fakeRDS{endpoints: map[[2]string]bool{{proxy, endpoint}: true}}
				mg := newManaged(scope, proxy, observeOnly)
				o := coldObserve(t, withFakeAPI(r, api), mg)
				if got, want := api.lastRead(), proxy+"/"+proxy; got != want {
					t.Errorf("Read ID = %q, want %q", got, want)
				}
				if o.exists {
					t.Errorf("ResourceExists = true, want false")
				}
				if got := meta.GetExternalName(mg); got != proxy {
					t.Errorf("external name = %q, want %q (unchanged)", got, proxy)
				}
				if len(api.creates) != 0 {
					t.Errorf("CreateDBProxyEndpoint calls = %v, want none", api.creates)
				}
			})
		})
	}
}
