package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A provider is a DEFINITION -- "here is how to connect to Acme" -- not a
// connection. Three tiers hold them: hostit's own catalog (in Go), the
// operator's (control.yml or this table with no owner), and a user's own.
func TestAProviderRoundTrips(t *testing.T) {
	s := newTestStore(t)
	p := &Provider{
		OwnerID: "u_1", Name: "acme", Label: "Acme", Kind: ProviderOAuth,
		Scopes: "read write", AuthURL: "https://acme/x", TokenURL: "https://acme/t",
		ClientID: "cid", ClientSecret: "sealed", CreatedAt: time.Now(),
	}
	require.NoError(t, s.AddProvider(p))
	require.NotEmpty(t, p.ID)

	got, err := s.ProviderByName("u_1", "acme")
	require.NoError(t, err)
	assert.Equal(t, "Acme", got.Label)
	assert.Equal(t, "sealed", got.ClientSecret)
	assert.Equal(t, "read write", got.Scopes)
}

// An instance provider has no owner and is visible to everyone; a personal one
// belongs to exactly one account. Listing for a user must return both, and
// never somebody else's.
func TestListingReturnsInstanceAndOwnProvidersOnly(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.AddProvider(&Provider{Name: "shared", Label: "Shared", Kind: ProviderOAuth, CreatedAt: time.Now()}))
	require.NoError(t, s.AddProvider(&Provider{OwnerID: "u_1", Name: "mine", Label: "Mine", Kind: ProviderOAuth, CreatedAt: time.Now()}))
	require.NoError(t, s.AddProvider(&Provider{OwnerID: "u_2", Name: "theirs", Label: "Theirs", Kind: ProviderOAuth, CreatedAt: time.Now()}))

	mine, err := s.ProvidersFor("u_1")
	require.NoError(t, err)
	names := make([]string, 0, len(mine))
	for _, p := range mine {
		names = append(names, p.Name)
	}
	assert.ElementsMatch(t, []string{"shared", "mine"}, names)

	instance, err := s.InstanceProviders()
	require.NoError(t, err)
	require.Len(t, instance, 1)
	assert.Equal(t, "shared", instance[0].Name)
}

// Two people may each call theirs "acme" -- they are different definitions in
// different namespaces. Refusing that would make one user's choice of name
// deny it to everybody else on the instance.
func TestTwoUsersCanEachHaveAProviderOfTheSameName(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.AddProvider(&Provider{OwnerID: "u_1", Name: "acme", Label: "A", Kind: ProviderOAuth, CreatedAt: time.Now()}))
	require.NoError(t, s.AddProvider(&Provider{OwnerID: "u_2", Name: "acme", Label: "B", Kind: ProviderOAuth, CreatedAt: time.Now()}))

	one, err := s.ProviderByName("u_1", "acme")
	require.NoError(t, err)
	assert.Equal(t, "A", one.Label)
	two, err := s.ProviderByName("u_2", "acme")
	require.NoError(t, err)
	assert.Equal(t, "B", two.Label)
}

// The same person twice is a different matter: it is one namespace and the
// second would silently replace what their apps mean by that name.
func TestOneUserCannotReuseTheirOwnProviderName(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.AddProvider(&Provider{OwnerID: "u_1", Name: "acme", Label: "A", Kind: ProviderOAuth, CreatedAt: time.Now()}))
	err := s.AddProvider(&Provider{OwnerID: "u_1", Name: "acme", Label: "B", Kind: ProviderOAuth, CreatedAt: time.Now()})
	assert.ErrorIs(t, err, ErrProviderExists)
}

// An instance provider wins over a personal one of the same name: the operator's
// definition is the one everybody else already means.
func TestAnInstanceProviderIsFoundWhenTheUserHasNoneOfThatName(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.AddProvider(&Provider{Name: "acme", Label: "Instance", Kind: ProviderOAuth, CreatedAt: time.Now()}))
	got, err := s.ProviderByName("u_1", "acme")
	require.NoError(t, err)
	assert.Equal(t, "Instance", got.Label)
	assert.Empty(t, got.OwnerID)
}

// MCP presets live in the same table: an operator offering "Linear" so a user
// does not have to know the URL is the same kind of thing as offering an OAuth
// provider.
func TestAnMCPPresetIsAProviderToo(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.AddProvider(&Provider{
		Name: "linear-mcp", Label: "Linear", Kind: ProviderMCP,
		URL: "https://mcp.linear.app/mcp", CreatedAt: time.Now(),
	}))
	got, err := s.ProviderByName("u_1", "linear-mcp")
	require.NoError(t, err)
	assert.Equal(t, ProviderMCP, got.Kind)
	assert.Equal(t, "https://mcp.linear.app/mcp", got.URL)
}

func TestUpdateAndDeleteAProvider(t *testing.T) {
	s := newTestStore(t)
	p := &Provider{OwnerID: "u_1", Name: "acme", Label: "A", Kind: ProviderOAuth, ClientSecret: "old", CreatedAt: time.Now()}
	require.NoError(t, s.AddProvider(p))

	p.Label = "Renamed"
	p.ClientSecret = "new"
	require.NoError(t, s.UpdateProvider(p))
	got, err := s.ProviderByName("u_1", "acme")
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Label)
	assert.Equal(t, "new", got.ClientSecret)

	require.NoError(t, s.DeleteProvider(p.ID))
	_, err = s.ProviderByName("u_1", "acme")
	assert.ErrorIs(t, err, ErrProviderNotFound)
}
