package mas

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/VeniVidiVici/envctl/internal/model"
)

type PreflightReport struct {
	MASVersion         string         `json:"mas_version"`
	Store              string         `json:"store"`
	Region             string         `json:"region"`
	MacOS              string         `json:"macos"`
	NoninteractiveRoot bool           `json:"noninteractive_root"`
	AccountStatus      string         `json:"account_status"`
	Apps               []AppPreflight `json:"apps"`
	Blockers           []string       `json:"blockers"`
	Warnings           []string       `json:"warnings,omitempty"`
	Ready              bool           `json:"ready"`
}

type AppPreflight struct {
	PackageID        string   `json:"package_id"`
	AdamID           string   `json:"adam_id"`
	Name             string   `json:"name,omitempty"`
	Version          string   `json:"version,omitempty"`
	FormattedPrice   string   `json:"formatted_price,omitempty"`
	Price            float64  `json:"price"`
	MinimumOS        string   `json:"minimum_os,omitempty"`
	Available        bool     `json:"available"`
	Compatible       bool     `json:"compatible"`
	CandidateCommand []string `json:"candidate_command,omitempty"`
	Blockers         []string `json:"blockers,omitempty"`
}

type configRecord struct {
	MAS    string `json:"mas"`
	Store  string `json:"store"`
	Region string `json:"region"`
	MacOS  string `json:"macos"`
}

type lookupRecord struct {
	AdamID           int64   `json:"adamID"`
	Name             string  `json:"name"`
	Version          string  `json:"version"`
	FormattedPrice   string  `json:"formattedPrice"`
	Price            float64 `json:"price"`
	MinimumOSVersion string  `json:"minimumOSVersion"`
}

func Preflight(
	ctx context.Context,
	actions []model.Action,
	runner Runner,
) (PreflightReport, error) {
	rawConfig, err := runner.Output(ctx, "mas", "config", "--json")
	if err != nil {
		return PreflightReport{}, fmt.Errorf("inspect mas configuration: %w", err)
	}
	var config configRecord
	if err := json.Unmarshal(rawConfig, &config); err != nil {
		return PreflightReport{}, fmt.Errorf("decode mas configuration: %w", err)
	}
	if config.MAS == "" || config.Store == "" || config.MacOS == "" {
		return PreflightReport{}, fmt.Errorf("mas configuration is incomplete")
	}

	report := PreflightReport{
		MASVersion:    config.MAS,
		Store:         config.Store,
		Region:        config.Region,
		MacOS:         config.MacOS,
		AccountStatus: "unknown",
	}
	if _, err := runner.Output(ctx, "sudo", "-n", "true"); err == nil {
		report.NoninteractiveRoot = true
	} else {
		report.Blockers = append(report.Blockers,
			"non-interactive sudo is unavailable; mas would require a password prompt")
	}
	report.Blockers = append(report.Blockers,
		"App Store Apple Account sign-in and per-app authorization cannot be verified noninteractively")

	for _, action := range actions {
		app := AppPreflight{
			PackageID: action.PackageID,
			AdamID:    action.Package,
		}
		if action.Manager != model.ManagerMAS ||
			action.Kind != model.KindApp ||
			action.Source != "mac-app-store" ||
			!validAdamID(action.Package) {
			app.Blockers = append(app.Blockers,
				"action is not a valid Mac App Store identity")
			report.Apps = append(report.Apps, app)
			continue
		}
		rawLookup, err := runner.Output(
			ctx, "mas", "lookup", "--json", action.Package,
		)
		if err != nil {
			app.Blockers = append(app.Blockers,
				"app is unavailable in the configured storefront")
			report.Apps = append(report.Apps, app)
			continue
		}
		var lookup lookupRecord
		if err := json.Unmarshal(rawLookup, &lookup); err != nil {
			return PreflightReport{}, fmt.Errorf(
				"decode App Store lookup for %s: %w", action.Package, err,
			)
		}
		if strconv.FormatInt(lookup.AdamID, 10) != action.Package {
			app.Blockers = append(app.Blockers,
				"App Store lookup returned a different identity")
			report.Apps = append(report.Apps, app)
			continue
		}
		app.Name = lookup.Name
		app.Version = lookup.Version
		app.FormattedPrice = lookup.FormattedPrice
		app.Price = lookup.Price
		app.MinimumOS = lookup.MinimumOSVersion
		app.Available = true
		app.Compatible = osVersionAtLeast(
			firstField(config.MacOS), lookup.MinimumOSVersion,
		)
		if !app.Compatible {
			app.Blockers = append(app.Blockers,
				fmt.Sprintf("requires macOS %s or newer", lookup.MinimumOSVersion))
		}
		if lookup.Price > 0 {
			app.Blockers = append(app.Blockers,
				"paid apps cannot be purchased by mas; ownership must be confirmed in App Store")
			app.CandidateCommand = []string{
				"sudo", "-n", "mas", "install", action.Package,
			}
		} else {
			app.CandidateCommand = []string{
				"sudo", "-n", "mas", "get", action.Package,
			}
		}
		report.Apps = append(report.Apps, app)
	}

	for _, app := range report.Apps {
		if len(app.Blockers) > 0 {
			report.Blockers = append(report.Blockers,
				fmt.Sprintf("%s (%s) has %d app-specific blocker(s)",
					app.PackageID, app.AdamID, len(app.Blockers)))
		}
	}
	report.Warnings = append(report.Warnings,
		"candidate commands are informational only and were not executed")
	report.Ready = len(report.Blockers) == 0
	return report, nil
}

func validAdamID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func firstField(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func osVersionAtLeast(installed, required string) bool {
	left, leftOK := numericVersion(installed)
	right, rightOK := numericVersion(required)
	if !leftOK || !rightOK {
		return false
	}
	width := max(len(left), len(right))
	for index := 0; index < width; index++ {
		var leftPart, rightPart int
		if index < len(left) {
			leftPart = left[index]
		}
		if index < len(right) {
			rightPart = right[index]
		}
		if leftPart != rightPart {
			return leftPart > rightPart
		}
	}
	return true
}

func numericVersion(value string) ([]int, bool) {
	parts := strings.Split(value, ".")
	if len(parts) == 0 {
		return nil, false
	}
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return nil, false
		}
		numbers = append(numbers, number)
	}
	return numbers, true
}
