package metrics

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type ChurnKind string

const (
	ChurnNone        ChurnKind = ""
	ChurnVoluntary   ChurnKind = "voluntary"
	ChurnInvoluntary ChurnKind = "involuntary"
)

type MonthlyRecurringRevenue struct {
	Month          time.Time
	OrganizationID string
	Currency       string
	Amount         int64
	ChurnKind      ChurnKind
}

type MRRMovement struct {
	Month                           time.Time
	Currency                        string
	OpeningMRR                      int64
	NewMRR                          int64
	ExpansionMRR                    int64
	ContractionMRR                  int64
	ChurnedMRR                      int64
	ReactivatedMRR                  int64
	ClosingMRR                      int64
	ARR                             int64
	PayingOrganizations             int
	NewOrganizations                int
	ChurnedOrganizations            int
	ReactivatedOrganizations        int
	VoluntaryChurnedOrganizations   int
	InvoluntaryChurnedOrganizations int
	ARPA                            float64
	GRR                             *float64
	NRR                             *float64
}

type organizationMonth struct {
	amount    int64
	churnKind ChurnKind
}

func MRRWaterfall(rows []MonthlyRecurringRevenue) ([]MRRMovement, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	currency := ""
	byMonth := map[time.Time]map[string]organizationMonth{}
	for _, row := range rows {
		if row.OrganizationID == "" {
			return nil, errors.New("metrics: organization ID is required")
		}
		if row.Amount < 0 {
			return nil, errors.New("metrics: normalized MRR cannot be negative")
		}
		if !isUTCMonth(row.Month) {
			return nil, errors.New("metrics: MRR month must be a UTC calendar-month boundary")
		}
		normalizedCurrency := strings.ToUpper(strings.TrimSpace(row.Currency))
		if normalizedCurrency == "" {
			return nil, errors.New("metrics: currency is required")
		}
		if currency != "" && currency != normalizedCurrency {
			return nil, errors.New("metrics: multi-currency MRR must be converted before aggregation")
		}
		currency = normalizedCurrency
		if row.ChurnKind != ChurnNone && row.ChurnKind != ChurnVoluntary &&
			row.ChurnKind != ChurnInvoluntary {
			return nil, fmt.Errorf("metrics: unknown churn kind %q", row.ChurnKind)
		}
		organizations := byMonth[row.Month]
		if organizations == nil {
			organizations = map[string]organizationMonth{}
			byMonth[row.Month] = organizations
		}
		if _, exists := organizations[row.OrganizationID]; exists {
			return nil, fmt.Errorf(
				"metrics: duplicate MRR row for organization %q in %s",
				row.OrganizationID,
				row.Month.Format("2006-01"),
			)
		}
		organizations[row.OrganizationID] = organizationMonth{
			amount:    row.Amount,
			churnKind: row.ChurnKind,
		}
	}

	months := make([]time.Time, 0, len(byMonth))
	for month := range byMonth {
		months = append(months, month)
	}
	sort.Slice(months, func(i, j int) bool { return months[i].Before(months[j]) })
	first, last := months[0], months[len(months)-1]
	everPaid := map[string]bool{}
	previous := map[string]organizationMonth{}
	waterfall := make([]MRRMovement, 0, monthsBetween(first, last)+1)

	for month := first; !month.After(last); month = month.AddDate(0, 1, 0) {
		current := byMonth[month]
		if current == nil {
			current = map[string]organizationMonth{}
		}
		movement := MRRMovement{Month: month, Currency: currency}
		organizations := make(map[string]struct{}, len(previous)+len(current))
		for organizationID := range previous {
			organizations[organizationID] = struct{}{}
		}
		for organizationID := range current {
			organizations[organizationID] = struct{}{}
		}
		for organizationID := range organizations {
			before := previous[organizationID]
			after := current[organizationID]
			movement.OpeningMRR += before.amount
			movement.ClosingMRR += after.amount
			if after.amount > 0 {
				movement.PayingOrganizations++
			}
			switch {
			case before.amount == 0 && after.amount > 0 && everPaid[organizationID]:
				movement.ReactivatedMRR += after.amount
				movement.ReactivatedOrganizations++
			case before.amount == 0 && after.amount > 0:
				movement.NewMRR += after.amount
				movement.NewOrganizations++
			case before.amount > 0 && after.amount == 0:
				movement.ChurnedMRR += before.amount
				movement.ChurnedOrganizations++
				switch after.churnKind {
				case ChurnVoluntary:
					movement.VoluntaryChurnedOrganizations++
				case ChurnInvoluntary:
					movement.InvoluntaryChurnedOrganizations++
				}
			case after.amount > before.amount:
				movement.ExpansionMRR += after.amount - before.amount
			case after.amount < before.amount:
				movement.ContractionMRR += before.amount - after.amount
			}
			if after.amount > 0 {
				everPaid[organizationID] = true
			}
		}
		movement.ARR = movement.ClosingMRR * 12
		if movement.PayingOrganizations > 0 {
			movement.ARPA = float64(movement.ClosingMRR) / float64(movement.PayingOrganizations)
		}
		if movement.OpeningMRR > 0 {
			grr := float64(movement.OpeningMRR-movement.ContractionMRR-movement.ChurnedMRR) /
				float64(movement.OpeningMRR)
			nrr := float64(
				movement.OpeningMRR+
					movement.ExpansionMRR+
					movement.ReactivatedMRR-
					movement.ContractionMRR-
					movement.ChurnedMRR,
			) / float64(movement.OpeningMRR)
			movement.GRR = &grr
			movement.NRR = &nrr
		}
		waterfall = append(waterfall, movement)
		previous = current
	}
	return waterfall, nil
}

func isUTCMonth(value time.Time) bool {
	return value.Location() == time.UTC &&
		value.Day() == 1 &&
		value.Hour() == 0 &&
		value.Minute() == 0 &&
		value.Second() == 0 &&
		value.Nanosecond() == 0
}

func monthsBetween(from, to time.Time) int {
	return (to.Year()-from.Year())*12 + int(to.Month()-from.Month())
}
