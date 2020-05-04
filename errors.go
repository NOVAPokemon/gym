package main

import (
	"fmt"
	"github.com/NOVAPokemon/utils"
	"github.com/pkg/errors"
)

const (
	errorLoadGyms                  = "error loading gyms"
	errorLoadSpecies               = "error loading pokemon species"
	errorTokensBattle = " error extracting and verifying tokens for battle"

	errorGymNoRaidBossFormat      = "gym %s has no raid boss"
	errorRaidAlreadExistsFormat   = "raid %s already created"
	errorRaidBossDeadFormat       = "gym %s, raid boss dead"
	errorNoGymFoundFormat         = "no gym found with id %s"
	errorNoRaidInGymFormat        = "no raid in gym %s"
	errorRaidAlreadyStartedFormat = "raid already started in gym %s"
)

var (
	ErrorNotEnoughPokemons    = errors.New("not enough pokemons")
	ErrorTooManyPokemons      = errors.New("not enough pokemons")
)

// Wrappers handlers
func wrapCreateGymError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, CreateGymName))
}

func wrapCreateRaidError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, CreateRaidName))
}

func wrapJoinRaidError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, JoinRaidName))
}

func wrapGetGymInfoError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, GetGymInfoName))
}

// Wrappers other functions
func wrapLoadGymsError(err error) error {
	return errors.Wrap(err, errorLoadGyms)
}

func wrapLoadSpecies(err error) error {
	return errors.Wrap(err, errorLoadSpecies)
}

func wrapTokensForBattleError(err error) error {
	return errors.Wrap(err, errorTokensBattle)
}

// Errors builders
func newGymNoRaidBossError(gymId string) error {
	return errors.New(fmt.Sprintf(errorGymNoRaidBossFormat, gymId))
}

func newRaidAlreadyExistsError(gymId string) error {
	return errors.New(fmt.Sprintf(errorRaidAlreadExistsFormat, gymId))
}

func newRaidBossDeadError(gymId string) error {
	return errors.New(fmt.Sprintf(errorRaidBossDeadFormat, gymId))
}

func newNoGymFoundError(gymId string) error {
	return errors.New(fmt.Sprintf(errorNoGymFoundFormat, gymId))
}

func newNoRaidInGymError(gymId string) error {
	return errors.New(fmt.Sprintf(errorNoRaidInGymFormat, gymId))
}

func newRaidAlreadyStartedError(gymId string) error {
	return errors.New(fmt.Sprintf(errorRaidAlreadyStartedFormat, gymId))
}
