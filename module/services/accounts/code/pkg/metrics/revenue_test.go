package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMRRWaterfall(t *testing.T) {
	january := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	february := january.AddDate(0, 1, 0)
	march := january.AddDate(0, 2, 0)
	april := january.AddDate(0, 3, 0)

	waterfall, err := MRRWaterfall([]MonthlyRecurringRevenue{
		{Month: january, OrganizationID: "alpha", Currency: "usd", Amount: 10000},
		{Month: january, OrganizationID: "bravo", Currency: "usd", Amount: 20000},
		{Month: february, OrganizationID: "alpha", Currency: "usd", Amount: 15000},
		{Month: february, OrganizationID: "bravo", Currency: "usd", Amount: 0, ChurnKind: ChurnVoluntary},
		{Month: february, OrganizationID: "charlie", Currency: "usd", Amount: 5000},
		{Month: march, OrganizationID: "alpha", Currency: "usd", Amount: 12000},
		{Month: march, OrganizationID: "bravo", Currency: "usd", Amount: 0},
		{Month: march, OrganizationID: "charlie", Currency: "usd", Amount: 5000},
		{Month: april, OrganizationID: "alpha", Currency: "usd", Amount: 12000},
		{Month: april, OrganizationID: "bravo", Currency: "usd", Amount: 8000},
		{Month: april, OrganizationID: "charlie", Currency: "usd", Amount: 0, ChurnKind: ChurnInvoluntary},
	}, april)
	require.NoError(t, err)
	require.Len(t, waterfall, 4)

	require.Equal(t, int64(30000), waterfall[0].NewMRR)
	require.Nil(t, waterfall[0].GRR)
	require.Equal(t, int64(30000), waterfall[1].OpeningMRR)
	require.Equal(t, int64(5000), waterfall[1].NewMRR)
	require.Equal(t, int64(5000), waterfall[1].ExpansionMRR)
	require.Equal(t, int64(20000), waterfall[1].ChurnedMRR)
	require.Equal(t, 1, waterfall[1].VoluntaryChurnedOrganizations)
	require.InDelta(t, 1.0/3.0, *waterfall[1].GRR, 0.000001)
	require.InDelta(t, 0.5, *waterfall[1].NRR, 0.000001)

	require.Equal(t, int64(3000), waterfall[2].ContractionMRR)
	require.Equal(t, int64(17000), waterfall[2].ClosingMRR)
	require.Equal(t, int64(204000), waterfall[2].ARR)

	require.Equal(t, int64(8000), waterfall[3].ReactivatedMRR)
	require.Equal(t, int64(5000), waterfall[3].ChurnedMRR)
	require.Equal(t, 1, waterfall[3].InvoluntaryChurnedOrganizations)
	require.Equal(t, 2, waterfall[3].PayingOrganizations)
	require.Equal(t, 10000.0, waterfall[3].ARPA)
	require.InDelta(t, 20.0/17.0, *waterfall[3].NRR, 0.000001)
}

func TestMRRWaterfallRejectsAmbiguousInputs(t *testing.T) {
	month := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err := MRRWaterfall([]MonthlyRecurringRevenue{
		{Month: month, OrganizationID: "alpha", Currency: "USD", Amount: 100},
		{Month: month, OrganizationID: "bravo", Currency: "EUR", Amount: 100},
	}, month)
	require.ErrorContains(t, err, "multi-currency")

	_, err = MRRWaterfall([]MonthlyRecurringRevenue{
		{Month: month, OrganizationID: "alpha", Currency: "USD", Amount: 100},
		{Month: month, OrganizationID: "alpha", Currency: "USD", Amount: 100},
	}, month)
	require.ErrorContains(t, err, "duplicate")
}

func TestMRRWaterfallIncludesTerminalChurnWithAnExplicitCause(t *testing.T) {
	january := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	february := january.AddDate(0, 1, 0)

	waterfall, err := MRRWaterfall([]MonthlyRecurringRevenue{
		{Month: january, OrganizationID: "alpha", Currency: "USD", Amount: 100},
		{
			Month: february, OrganizationID: "alpha", Currency: "USD",
			Amount: 0, ChurnKind: ChurnVoluntary,
		},
	}, february)

	require.NoError(t, err)
	require.Len(t, waterfall, 2)
	require.Equal(t, int64(100), waterfall[1].ChurnedMRR)
	require.Equal(t, 1, waterfall[1].VoluntaryChurnedOrganizations)
}

func TestMRRWaterfallRejectsMissingOrUnclassifiedSnapshots(t *testing.T) {
	january := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	february := january.AddDate(0, 1, 0)

	_, err := MRRWaterfall([]MonthlyRecurringRevenue{
		{Month: january, OrganizationID: "alpha", Currency: "USD", Amount: 100},
	}, february)
	require.ErrorContains(t, err, "missing MRR snapshot")

	_, err = MRRWaterfall([]MonthlyRecurringRevenue{
		{Month: january, OrganizationID: "alpha", Currency: "USD", Amount: 100},
		{Month: february, OrganizationID: "alpha", Currency: "USD", Amount: 0},
	}, february)
	require.ErrorContains(t, err, "requires a cause")

	_, err = MRRWaterfall([]MonthlyRecurringRevenue{
		{Month: february, OrganizationID: "alpha", Currency: "USD", Amount: 100},
	}, january)
	require.ErrorContains(t, err, "after the waterfall end")
}
