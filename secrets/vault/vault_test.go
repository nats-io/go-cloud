// Copyright 2019 The Go Cloud Development Kit Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limtations under the License.

package vault

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/builtin/logical/transit"
	vhttp "github.com/hashicorp/vault/http"
	"github.com/hashicorp/vault/logical"
	"github.com/hashicorp/vault/vault"
	"gocloud.dev/secrets"
	"gocloud.dev/secrets/driver"
	"gocloud.dev/secrets/drivertest"
)

const (
	keyID1     = "test-secrets"
	keyID2     = "test-secrets2"
	apiAddress = "http://127.0.0.1:0"
)

type harness struct {
	client *api.Client
	close  func()
}

func (h *harness) MakeDriver(ctx context.Context) (driver.Keeper, driver.Keeper, error) {
	return &keeper{keyID: keyID1, client: h.client}, &keeper{keyID: keyID2, client: h.client}, nil
}

func (h *harness) Close() {
	h.close()
}

func newHarness(ctx context.Context, t *testing.T) (drivertest.Harness, error) {
	// Start a new test server.
	c, cleanup := testVaultServer(t)
	// Enable the Transit Secrets Engine to use Vault as an Encryption as a Service.
	c.Logical().Write("sys/mounts/transit", map[string]interface{}{
		"type": "transit",
	})

	return &harness{
		client: c,
		close:  cleanup,
	}, nil
}

func testVaultServer(t *testing.T) (*api.Client, func()) {
	coreCfg := &vault.CoreConfig{
		DisableMlock: true,
		DisableCache: true,
		// Enable the testing transit backend.
		LogicalBackends: map[string]logical.Factory{
			"transit": transit.Factory,
		},
	}
	cluster := vault.NewTestCluster(t, coreCfg, &vault.TestClusterOptions{
		HandlerFunc: vhttp.Handler,
	})
	cluster.Start()
	tc := cluster.Cores[0]
	vault.TestWaitActive(t, tc.Core)

	tc.Client.SetToken(cluster.RootToken)
	return tc.Client, cluster.Cleanup
}

func TestConformance(t *testing.T) {
	drivertest.RunConformanceTests(t, newHarness, []drivertest.AsTest{verifyAs{}})
}

type verifyAs struct{}

func (v verifyAs) Name() string {
	return "verify As function"
}

func (v verifyAs) ErrorCheck(k *secrets.Keeper, err error) error {
	var s string
	if k.ErrorAs(err, &s) {
		return errors.New("Keeper.ErrorAs expected to fail")
	}
	return nil
}

// Vault-specific tests.

func TestNoSessionProvidedError(t *testing.T) {
	if _, err := Dial(context.Background(), nil); err == nil {
		t.Error("got nil, want no auth Config provided")
	}
}

func TestNoConnectionError(t *testing.T) {
	ctx := context.Background()

	// Dial calls vault's NewClient method, which doesn't make the connection. Try
	// doing encryption which should fail by no connection.
	client, err := Dial(ctx, &Config{
		Token: "<Client (Root) Token>",
		APIConfig: api.Config{
			Address: apiAddress,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	keeper := NewKeeper(client, "my-key", nil)
	if _, err := keeper.Encrypt(ctx, []byte("test")); err == nil {
		t.Error("got nil, want connection refused")
	}
}

func TestURLCaching(t *testing.T) {

	tests := []struct {
		URL  string
		Want int
	}{
		{
			URL:  "vault://mykey?address=foo&token=bar",
			Want: 1,
		},
		// Cached.
		{
			URL:  "vault://mykey?address=foo&token=bar",
			Want: 1,
		},
		// Still cached despite parameter order change.
		{
			URL:  "vault://mykey?token=bar&address=foo",
			Want: 1,
		},
		// Still cached despite key change.
		{
			URL:  "vault://anotherkey?token=bar&address=foo",
			Want: 1,
		},
		// Still cached despite extra parameter.
		{
			URL:  "vault://anotherkey?token=bar&address=foo&someparam=somevalue",
			Want: 1,
		},
		// New token.
		{
			URL:  "vault://mykey?token=newtoken&address=foo",
			Want: 2,
		},
		// Old is still cached.
		{
			URL:  "vault://mykey?address=foo&token=bar",
			Want: 2,
		},
		// And new is cached.
		{
			URL:  "vault://mykey?token=newtoken&address=foo",
			Want: 2,
		},
		// New address.
		{
			URL:  "vault://mykey?token=bar&address=newaddress",
			Want: 3,
		},
	}

	ctx := context.Background()
	o := &lazyDialer{}
	for i, test := range tests {
		u, err := url.Parse(test.URL)
		if err != nil {
			t.Fatal(err)
		}
		o.cachedClient(ctx, u)
		if got := len(o.clients); got != test.Want {
			t.Errorf("%d/%s: got %d want %d", i, test.URL, got, test.Want)
		}
	}
}

func TestOpenKeeper(t *testing.T) {
	tests := []struct {
		URL     string
		WantErr bool
	}{
		{"vault://mykey?token=bar&address=address", false},
		{"vault://mykey?token=bar&token=token", false},
		{"vault://mykey?token=bar&address=address&token=token", false},
		{"vault://mykey?token=bar&param=value", true},
	}

	ctx := context.Background()
	for _, test := range tests {
		_, err := secrets.OpenKeeper(ctx, test.URL)
		if (err != nil) != test.WantErr {
			t.Errorf("%s: got error %v, want error %v", test.URL, err, test.WantErr)
		}
	}
}
