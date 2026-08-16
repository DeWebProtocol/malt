package main

import (
	"strings"
	"testing"

	clientadd "github.com/dewebprotocol/malt-client/application/add"
	"github.com/dewebprotocol/malt-client/unixfs"
)

func TestBindAddLayoutToBucketUsesPersistedLayoutAndRejectsConflicts(t *testing.T) {
	for _, test := range []struct {
		name     string
		layout   string
		explicit bool
		bucket   unixfs.LayoutKind
		want     string
		wantErr  bool
	}{
		{name: "default becomes Flat", layout: clientadd.LayoutHybrid, bucket: unixfs.LayoutFlatV1, want: clientadd.LayoutFlatV1},
		{name: "explicit Flat matches", layout: clientadd.LayoutFlatV1, explicit: true, bucket: unixfs.LayoutFlatV1, want: clientadd.LayoutFlatV1},
		{name: "hybrid alias matches Hybrid", layout: clientadd.LayoutHybrid, explicit: true, bucket: unixfs.LayoutHybridV1, want: clientadd.LayoutHybridV1},
		{name: "explicit mismatch", layout: clientadd.LayoutHybridV1, explicit: true, bucket: unixfs.LayoutFlatV1, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := bindAddLayoutToBucket(
				clientadd.Options{Target: clientadd.TargetMALT, Layout: test.layout},
				test.explicit,
				test.bucket,
			)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "conflicts with selected Bucket layout") {
					t.Fatalf("bind error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Layout != test.want {
				t.Fatalf("layout = %q, want %q", got.Layout, test.want)
			}
		})
	}
}
