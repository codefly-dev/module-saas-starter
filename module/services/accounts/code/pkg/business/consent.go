package business

import (
	"context"
	"time"

	"github.com/codefly-dev/core/wool"
)

const CurrentTermsVersion = "2026-07-28"

type ConsentPreference struct {
	Purpose       string
	Granted       bool
	PolicyVersion string
	UpdatedAt     time.Time
	WithdrawnAt   *time.Time
}

type UserConsentStatus struct {
	TermsAcceptedVersion     string
	TermsAcceptedAt          *time.Time
	CurrentTermsVersion      string
	PolicyVersion            string
	PreferencesRecorded      bool
	PreferencesPolicyVersion string
	Preferences              []*ConsentPreference
}

func (s *Service) GetConsentStatus(
	ctx context.Context,
	userID string,
) (*UserConsentStatus, error) {
	var termsVersion string
	var termsAt *time.Time
	var preferences []*ConsentPreference
	err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		var err error
		termsVersion, termsAt, err = s.store.GetUserConsent(ctx, userID)
		if err != nil {
			return err
		}
		preferences, err = s.store.GetUserConsentPreferences(ctx, userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	preferencesRecorded := len(preferences) > 0
	preferencesPolicyVersion := ""
	preferenceMap := make(map[string]*ConsentPreference, len(preferences))
	for _, preference := range preferences {
		preferenceMap[preference.Purpose] = preference
		if preferencesPolicyVersion == "" {
			preferencesPolicyVersion = preference.PolicyVersion
		}
	}
	now := time.Now()
	necessary := preferenceMap["necessary"]
	if necessary == nil {
		necessary = &ConsentPreference{
			Purpose:       "necessary",
			Granted:       true,
			PolicyVersion: CurrentConsentPolicyVersion,
			UpdatedAt:     now,
		}
	}
	analytics := preferenceMap["analytics"]
	if analytics == nil {
		analytics = &ConsentPreference{
			Purpose:       "analytics",
			Granted:       false,
			PolicyVersion: CurrentConsentPolicyVersion,
			UpdatedAt:     now,
		}
	}
	marketing := preferenceMap["marketing"]
	if marketing == nil {
		marketing = &ConsentPreference{
			Purpose:       "marketing",
			Granted:       false,
			PolicyVersion: CurrentConsentPolicyVersion,
			UpdatedAt:     now,
		}
	}
	return &UserConsentStatus{
		TermsAcceptedVersion:     termsVersion,
		TermsAcceptedAt:          termsAt,
		CurrentTermsVersion:      CurrentTermsVersion,
		PolicyVersion:            CurrentConsentPolicyVersion,
		PreferencesRecorded:      preferencesRecorded,
		PreferencesPolicyVersion: preferencesPolicyVersion,
		Preferences:              []*ConsentPreference{necessary, analytics, marketing},
	}, nil
}

func (s *Service) AcceptTerms(
	ctx context.Context,
	userID, version string,
) error {
	if version != CurrentTermsVersion {
		return wool.Get(ctx).NewError("terms version is not current")
	}
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		return s.store.SetUserConsent(ctx, userID, version, time.Now())
	}); err != nil {
		return err
	}
	s.emit(ctx, userID, "user", "consent.terms_accepted", "user", userID, "")
	return nil
}

func (s *Service) UpdateConsentPreferences(
	ctx context.Context,
	userID, policyVersion string,
	analytics, marketing bool,
	region, consentContext string,
) error {
	if policyVersion != CurrentConsentPolicyVersion {
		return wool.Get(ctx).NewError("consent policy version is not current")
	}
	now := time.Now()
	preferences := []*ConsentPreference{
		{Purpose: "necessary", Granted: true, PolicyVersion: policyVersion, UpdatedAt: now},
		{Purpose: "analytics", Granted: analytics, PolicyVersion: policyVersion, UpdatedAt: now},
		{Purpose: "marketing", Granted: marketing, PolicyVersion: policyVersion, UpdatedAt: now},
	}
	if err := s.store.As(Identity{UserID: userID}).Within(ctx, func(ctx context.Context) error {
		return s.store.SetUserConsentPreferences(
			ctx, userID, preferences, region, consentContext,
		)
	}); err != nil {
		return err
	}
	s.emit(ctx, userID, "user", "consent.preferences_updated", "user", userID, "")
	return nil
}
