package format

import (
	"testing"

	"github.com/dewebprotocol/malt-core/derivation"
	maltcid "github.com/dewebprotocol/malt-core/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestStorageKindFromCIDUsesTypedLayout(t *testing.T) {
	mapRoot, err := maltcid.NewRoot(maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.KZG4096}, make([]byte, maltcid.KZGCommitmentSize))
	if err != nil {
		t.Fatal(err)
	}
	listRoot, err := maltcid.NewRoot(maltcid.RootDescriptor{DerivationProfile: uint8(derivation.Direct), Layout: maltcid.Positional, Profile: maltcid.IPA256}, make([]byte, maltcid.IPACommitmentSize))
	if err != nil {
		t.Fatal(err)
	}
	rawHash, err := mh.Sum([]byte("raw"), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	raw := cid.NewCidV1(cid.Raw, rawHash)
	manifestV1 := cid.NewCidV1(0x310001, rawHash)
	manifestV2 := cid.NewCidV1(CodecMaltManifestV2, rawHash)

	for name, test := range map[string]struct {
		cid  cid.Cid
		want string
	}{
		"undefined":           {cid: cid.Undef, want: ""},
		"raw":                 {cid: raw, want: "raw"},
		"retired-manifest-v1": {cid: manifestV1, want: ""},
		"manifest-v2":         {cid: manifestV2, want: "raw"},
		"prefix":              {cid: mapRoot, want: "prefix"},
		"positional":          {cid: listRoot, want: "positional"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := StorageKindFromCID(test.cid); got != test.want {
				t.Fatalf("StorageKindFromCID() = %q, want %q", got, test.want)
			}
		})
	}
}
