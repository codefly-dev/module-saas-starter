package business

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

func TestSignedSolutionGroupOrderIsRepresentedExactlyOrRefused(t *testing.T) {
	for _, order := range []int64{-2147483649, -2147483648, 2147483647, 2147483648} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			document := fixtureManifest(t, "grouped", 1)
			document.Kind = solutionhost.KindSolution
			document.Group = &solutionhost.SolutionGroup{ID: "operations", Title: "Operations", Order: 0}
			canonical, err := solutionhost.SignedPayload(document)
			require.NoError(t, err)
			// Sign the actual candidate integer, including values a compliant
			// producer refuses. Signature validity never waives schema checks.
			payload := strings.Replace(string(canonical), `"order":0`, fmt.Sprintf(`"order":%d`, order), 1)
			require.NotEqual(t, string(canonical), payload)
			verified, problems := verifySolutionHostBindingDocuments(context.Background(), fixtureSigner(t),
				[]SolutionHostBindingDocument{{Source: "group.codefly.yaml", Data: carrierOver(t, []byte(payload))}})
			if order < -2147483648 || order > 2147483647 {
				require.Empty(t, verified)
				require.Len(t, problems, 1)
				require.ErrorContains(t, problems[0].err, "group.order")
			} else {
				require.Empty(t, problems)
				require.Len(t, verified, 1)
				require.Equal(t, order, verified[0].Document.Group.Order)
				require.Equal(t, order, int64(int32(verified[0].Document.Group.Order)))
			}
		})
	}
}
