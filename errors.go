package main

import (
	"fmt"

	"github.com/NOVAPokemon/utils"
	"github.com/pkg/errors"
)

const (
	errorLoadSpecies  = "error loading pokemon species"
	errorTokensBattle = " error extracting and verifying tokens for battle"
	errorInit         = "error in init"

	errorGymNoRaidBossFormat    = "gym %s has no raid boss"
	errorRaidAlreadExistsFormat = "raid %s already created"
	errorRaidBossDeadFormat     = "gym %s, raid boss dead"
	errorNoGymFoundFormat       = "no gym found with id %s"
	errorNoRaidInGymFormat      = "no raid in gym %s"
	errorLoadingGymsFromDB      = "error loading gyms from DB"
	errorLoadingGymsToDB        = "error loading gyms to DB"
	errorJoiningRaid            = "error adding player to raid"
)

var (
	errorNotEnoughPokemons = errors.New("not enough pokemons")
	errorTooManyPokemons   = errors.New("too many pokemons")
)

func wrapInit(err error) error {
	return errors.Wrap(err, errorInit)
}

// Wrappers handlers.
func wrapCreateGymError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, createGymName))
}

func wrapCreateRaidError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, createRaidName))
}

func wrapJoinRaidError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, joinRaidName))
}

func wrapGetGymInfoError(err error) error {
	return errors.Wrap(err, fmt.Sprintf(utils.ErrorInHandlerFormat, getGymInfoName))
}

// Wrappers other functions.
func wrapLoadGymsFromDBError(err error) error {
	return errors.Wrap(err, errorLoadingGymsFromDB)
}

func wrapLoadGymsToDBError(err error) error {
	return errors.Wrap(err, errorLoadingGymsToDB)
}

func wrapLoadSpecies(err error) error {
	return errors.Wrap(err, errorLoadSpecies)
}

func wrapTokensForBattleError(err error) error {
	return errors.Wrap(err, errorTokensBattle)
}

func wrapRaidAddPlayerError(err error) error {
	return errors.Wrap(err, errorJoiningRaid)
}

// Errors builders

func newGymNoRaidBossError(gymID string) error {
	return fmt.Errorf(errorGymNoRaidBossFormat, gymID)
}

func newRaidAlreadyExistsError(gymID string) error {
	return fmt.Errorf(errorRaidAlreadExistsFormat, gymID)
}

func newRaidBossDeadError(gymID string) error {
	return fmt.Errorf(errorRaidBossDeadFormat, gymID)
}

func newNoGymFoundError(gymID string) error {
	return fmt.Errorf(errorNoGymFoundFormat, gymID)
}

func newNoRaidInGymError(gymID string) error {
	return fmt.Errorf(errorNoRaidInGymFormat, gymID)
}
