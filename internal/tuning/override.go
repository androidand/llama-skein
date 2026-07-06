package tuning

import "strconv"

// Override is the per-host user customization of a profile. It is the
// "recommended, never forced" layer: any field set here wins over the
// built-in profile (including turning a recommendation off), Enabled=false
// disables all injection, and ExtraArgs adds flags the curated profile does
// not model. A nil pointer field means "defer to the profile".
type Override struct {
	Enabled   *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	FlashAttn *bool    `yaml:"flash_attn,omitempty" json:"flash_attn,omitempty"`
	Parallel  *int     `yaml:"parallel,omitempty" json:"parallel,omitempty"`
	MTP       *bool    `yaml:"mtp,omitempty" json:"mtp,omitempty"`
	ExtraArgs []string `yaml:"extra_args,omitempty" json:"extra_args,omitempty"`
	GfxTarget string   `yaml:"gfx_target,omitempty" json:"gfx_target,omitempty"`
}

// Provenance labels where an effective value came from.
type Provenance string

const (
	FromProfile  Provenance = "recommended"
	FromOverride Provenance = "override"
)

// Effective is the resolved tuning for a host: the profile after applying the
// override, plus per-value provenance and the enabled flag. This is what the
// injector and the API report.
type Effective struct {
	Enabled   bool
	Profile   Profile
	ExtraArgs []string
	Source    map[string]Provenance // "flash_attn","parallel","mtp"
}

// Resolve overlays ovr onto base. base is the built-in profile (or a zero
// Profile if the gfx is unknown). Enabled defaults to true.
func Resolve(base Profile, ovr Override) Effective {
	eff := Effective{
		Enabled: true,
		Profile: base,
		Source:  map[string]Provenance{},
	}
	if ovr.Enabled != nil {
		eff.Enabled = *ovr.Enabled
	}

	// flash_attn
	if ovr.FlashAttn != nil {
		v := *ovr.FlashAttn
		eff.Profile.Flags.FlashAttn = &v
		eff.Source["flash_attn"] = FromOverride
	} else if base.Flags.FlashAttn != nil {
		eff.Source["flash_attn"] = FromProfile
	}

	// parallel
	if ovr.Parallel != nil {
		v := *ovr.Parallel
		eff.Profile.Flags.Parallel = &v
		eff.Source["parallel"] = FromOverride
	} else if base.Flags.Parallel != nil {
		eff.Source["parallel"] = FromProfile
	}

	// mtp: override can force-disable (mtp=false) or force-enable
	if ovr.MTP != nil {
		eff.Source["mtp"] = FromOverride
		if !*ovr.MTP {
			eff.Profile.MTP = nil // disabled: never inject spec flags
		} else if eff.Profile.MTP == nil {
			// force-enable even if the profile didn't recommend MTP
			eff.Profile.MTP = &MTP{ApplyToMTPModels: true, DraftNMax: 3, DraftPMin: 0.5}
		} else {
			m := *eff.Profile.MTP
			m.ApplyToMTPModels = true
			eff.Profile.MTP = &m
		}
	} else if base.MTP != nil {
		eff.Source["mtp"] = FromProfile
	}

	eff.ExtraArgs = ovr.ExtraArgs
	return eff
}

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
