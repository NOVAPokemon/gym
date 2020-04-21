package main

type GymServerConfig struct {
	DefaultCooldown int // milliseconds
	TimeToStartRaid int // milliseconds
	PokemonsPerRaid int

	MaxLevel           float64 `json:"max_level"`
	MaxHP              float64 `json:"max_hp"`
	MaxDamage          float64 `json:"max_damage"`
	StdHpDeviation     float64 `json:"stdHpDeviation"`
	StdDamageDeviation float64 `json:"stdDamageDeviation"`
}
