package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/protolambda/zrnt/eth2/beacon/common"
	"github.com/protolambda/zrnt/eth2/beacon/phase0"
	"github.com/tyler-smith/go-bip39"
	util "github.com/wealdtech/go-eth2-util"
	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

func loadValidatorKeys(spec *common.Spec, mnemonicsConfigPath string, validatorsListPath string, tranchesDir string) ([]phase0.KickstartValidatorData, error) {
	validators := []phase0.KickstartValidatorData{}

	if mnemonicsConfigPath != "" {
		val, err := generateValidatorKeysByMnemonic(spec, mnemonicsConfigPath, tranchesDir)
		if err != nil {
			fmt.Printf("error loading validators from mnemonic yaml (%s): %s\n", mnemonicsConfigPath, err)
		} else {
			fmt.Printf("generated %d validators from mnemonic yaml (%s)\n", len(val), mnemonicsConfigPath)
			validators = append(validators, val...)
		}
	}
	
	if validatorsListPath != "" {
		val, err := loadValidatorsFromFile(spec, validatorsListPath)
		if err != nil {
			fmt.Printf("error loading validators from validators list (%s): %s\n", validatorsListPath, err)
		} else {
			fmt.Printf("loaded %d validators from validators list (%s)\n", len(val), validatorsListPath)
			validators = append(validators, val...)
		}
	}

	return validators, nil
}

func generateValidatorKeysByMnemonic(spec *common.Spec, mnemonicsConfigPath string, tranchesDir string) ([]phase0.KickstartValidatorData, error) {
	mnemonics, err := loadMnemonics(mnemonicsConfigPath)
	if err != nil {
		return nil, err
	}

	//var validators []phase0.KickstartValidatorData
	var valCount uint64 = 0
	for _, mnemonicSrc := range mnemonics {
		valCount += mnemonicSrc.Count
	}
	validators := make([]phase0.KickstartValidatorData, valCount)

	offset := uint64(0)
	for m, mnemonicSrc := range mnemonics {
		var g errgroup.Group
		var prog int32
		fmt.Printf("processing mnemonic %d, for %d validators\n", m, mnemonicSrc.Count)
		seed, err := seedFromMnemonic(mnemonicSrc.Mnemonic)
		if err != nil {
			return nil, fmt.Errorf("mnemonic %d is bad", m)
		}
		pubs := make([]string, mnemonicSrc.Count)
		for i := uint64(0); i < mnemonicSrc.Count; i++ {
			valIndex := offset + i
			idx := i
			g.Go(func() error {
				signingKey, err := util.PrivateKeyFromSeedAndPath(seed, validatorKeyName(idx))
				if err != nil {
					return err
				}
				withdrawalKey, err := util.PrivateKeyFromSeedAndPath(seed, withdrawalKeyName(idx))
				if err != nil {
					return err
				}

				// BLS signing key
				var data phase0.KickstartValidatorData
				copy(data.Pubkey[:], signingKey.PublicKey().Marshal())
				pubs[idx] = data.Pubkey.String()

				// BLS withdrawal credentials
				h := sha256.New()
				h.Write(withdrawalKey.PublicKey().Marshal())
				copy(data.WithdrawalCredentials[:], h.Sum(nil))
				data.WithdrawalCredentials[0] = common.BLS_WITHDRAWAL_PREFIX

				// Max effective balance by default for activation
				data.Balance = spec.MAX_EFFECTIVE_BALANCE
				validators[valIndex] = data
				atomic.AddInt32(&prog, 1)
				if prog%100 == 0 {
					fmt.Printf("...validator %d/%d\n", prog, mnemonicSrc.Count)
				}
				return nil
			})

		}
		offset += mnemonicSrc.Count
		if err := g.Wait(); err != nil {
			return nil, err
		}

		fmt.Println("Writing pubkeys list file...")
		if err := outputPubkeys(filepath.Join(tranchesDir, fmt.Sprintf("tranche_%04d.txt", m)), pubs); err != nil {
			return nil, err
		}
	}
	return validators, nil
}

func validatorKeyName(i uint64) string {
	return fmt.Sprintf("m/12381/3600/%d/0/0", i)
}

func withdrawalKeyName(i uint64) string {
	return fmt.Sprintf("m/12381/3600/%d/0", i)
}

func seedFromMnemonic(mnemonic string) (seed []byte, err error) {
	mnemonic = strings.TrimSpace(mnemonic)
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, errors.New("mnemonic is not valid")
	}
	return bip39.NewSeed(mnemonic, ""), nil
}

func outputPubkeys(outPath string, data []string) error {
	f, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY, 0777)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, p := range data {
		if _, err := f.WriteString(p + "\n"); err != nil {
			return err
		}
	}
	return nil
}

type MnemonicSrc struct {
	Mnemonic string `yaml:"mnemonic"`
	Count    uint64 `yaml:"count"`
}

func loadMnemonics(srcPath string) ([]MnemonicSrc, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var data []MnemonicSrc
	dec := yaml.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func loadValidatorsFromFile(spec *common.Spec, validatorsConfigPath string) ([]phase0.KickstartValidatorData, error) {
	validatorsSrc, err := loadValidators(validatorsConfigPath)
	if err != nil {
		return nil, err
	}

	//var validators []phase0.KickstartValidatorData
	validators := make([]phase0.KickstartValidatorData, len(validatorsSrc))

	valIndex := uint64(0)
	for _, validatorSrc := range validatorsSrc {
		entry := strings.Split(validatorSrc, ":")
		
		// BLS signing key
		var data phase0.KickstartValidatorData

		// Public key
		pubKey, err := hex.DecodeString(strings.Replace(entry[0], "0x", "", -1))
		if err != nil {
				return nil, err
		}
		copy(data.Pubkey[:], pubKey)

		// Withdrawal credentials
		withdrawalCred, err := hex.DecodeString(strings.Replace(entry[1], "0x", "", -1))
		if err != nil {
				return nil, err
		}
		copy(data.WithdrawalCredentials[:], withdrawalCred)

		// Validator balance
		if len(entry) > 2 {
			balance, err := strconv.ParseUint(string(entry[2]), 10, 64)
			if err != nil {
				return nil, err
			}
			data.Balance = common.Gwei(balance)
		} else {
			data.Balance = spec.MAX_EFFECTIVE_BALANCE
		}
		
		validators[valIndex] = data
		valIndex++
	}
	return validators, nil
}

func loadValidators(srcPath string) ([]string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	lines := []string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			lines = append(lines, line)
		}
	}

	return lines, nil
}
