package commands_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-peer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/filecoin-project/go-filecoin/actor/builtin/miner"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/commands"
	"github.com/filecoin-project/go-filecoin/consensus"
	"github.com/filecoin-project/go-filecoin/fixtures"
	"github.com/filecoin-project/go-filecoin/gengen/util"
	th "github.com/filecoin-project/go-filecoin/testhelpers"
	tf "github.com/filecoin-project/go-filecoin/testhelpers/testflags"
	"github.com/filecoin-project/go-filecoin/types"
)

func TestMinerHelp(t *testing.T) {
	tf.IntegrationTest(t)

	t.Run("--help shows general miner help", func(t *testing.T) {

		expected := []string{
			"miner create <collateral>               - Create a new file miner with <collateral> FIL",
			"miner owner <miner>                     - Show the actor address of <miner>",
			"miner power <miner>                     - Get the power of a miner versus the total storage market power",
			"miner set-price <storageprice> <expiry> - Set the minimum price for storage",
			"miner update-peerid <address> <peerid>  - Change the libp2p identity that a miner is operating",
		}

		result := runHelpSuccess(t, "miner", "--help")
		for _, elem := range expected {
			assert.Contains(t, result, elem)
		}
	})

	t.Run("update-peerid --help shows update-peerid help", func(t *testing.T) {

		result := runHelpSuccess(t, "miner", "update-peerid", "--help")
		assert.Contains(t, result, "Issues a new message to the network to update the miner's libp2p identity.")
	})

	t.Run("owner --help shows owner help", func(t *testing.T) {

		result := runHelpSuccess(t, "miner", "owner", "--help")
		assert.Contains(t, result, "Given <miner> miner address, output the address of the actor that owns the miner.")
	})

	t.Run("create --help shows create help", func(t *testing.T) {

		expected := []string{
			"Issues a new message to the network to create the miner, then waits for the",
			"message to be mined as this is required to return the address of the new miner.",
			"Collateral will be committed at the rate of 0.001FIL per sector. When the",
			"miner's collateral drops below 0.001FIL, the miner will not be able to commit",
			"additional sectors.",
		}

		result := runHelpSuccess(t, "miner", "create", "--help")
		for _, elem := range expected {
			assert.Contains(t, result, elem)
		}
	})

}

func runHelpSuccess(t *testing.T, args ...string) string {
	fi, err := ioutil.TempFile("", "gengentest")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = gengen.GenGenesisCar(testConfig, fi, 0); err != nil {
		t.Fatal(err)
	}

	_ = fi.Close()
	d := th.NewDaemon(t, th.GenesisFile(fi.Name())).Start()
	defer d.ShutdownSuccess()

	op := d.RunSuccess(args...)
	return op.ReadStdoutTrimNewlines()
}

func TestMinerCreate(t *testing.T) {
	tf.IntegrationTest(t)

	testAddr, err := address.NewFromString(fixtures.TestAddresses[2])
	require.NoError(t, err)

	t.Run("success", func(t *testing.T) {

		var err error
		var addr address.Address

		tf := func(fromAddress address.Address, pid peer.ID) {
			d1 := makeTestDaemonWithMinerAndStart(t)
			defer d1.ShutdownSuccess()

			d := th.NewDaemon(t, th.KeyFile(fixtures.KeyFilePaths()[2])).Start()
			defer d.ShutdownSuccess()

			d1.ConnectSuccess(d)

			args := []string{"miner", "create", "--from", fromAddress.String(), "--gas-price", "1", "--gas-limit", "300"}

			if pid.Pretty() != peer.ID("").Pretty() {
				args = append(args, "--peerid", pid.Pretty())
			}

			collateral := miner.MinimumCollateralPerSector.CalculatePrice(types.NewBytesAmount(1000000 * types.OneKiBSectorSize.Uint64()))
			args = append(args, collateral.String())

			var wg sync.WaitGroup

			wg.Add(1)
			go func() {
				miner := d.RunSuccess(args...)
				addr, err = address.NewFromString(strings.Trim(miner.ReadStdout(), "\n"))
				assert.NoError(t, err)
				assert.NotEqual(t, addr, address.Undef)
				wg.Done()
			}()

			// ensure mining runs after the command in our goroutine
			d1.MineAndPropagate(time.Second, d)
			wg.Wait()

			// expect address to have been written in config
			config := d.RunSuccess("config mining.minerAddress")
			assert.Contains(t, config.ReadStdout(), addr.String())
		}

		tf(testAddr, peer.ID(""))

		// Will accept a peer ID if one is provided
		tf(testAddr, th.RequireRandomPeerID(t))
	})

	t.Run("unsupported sector size", func(t *testing.T) {
		d := th.NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		d.CreateAddress()

		d.RunFail("unsupported sector size",
			"miner", "create", "20",
			"--sectorsize", "42",
		)
	})

	t.Run("validation failure", func(t *testing.T) {

		d := th.NewDaemon(t).Start()
		defer d.ShutdownSuccess()

		d.CreateAddress()

		d.RunFail("invalid sector size",
			"miner", "create", "20",
			"--sectorsize", "ninetybillion",
		)
		d.RunFail("invalid peer id",
			"miner", "create",
			"--from", testAddr.String(), "--gas-price", "1", "--gas-limit", "300", "--peerid", "flarp", "20",
		)
		d.RunFail("invalid from address",
			"miner", "create",
			"--from", "hello", "--gas-price", "1", "--gas-limit", "1000000", "20",
		)
		d.RunFail("invalid collateral",
			"miner", "create",
			"--from", testAddr.String(), "--gas-price", "1", "--gas-limit", "100", "2f",
		)
	})
}

func TestMinerSetPrice(t *testing.T) {
	tf.IntegrationTest(t)

	d1 := th.NewDaemon(t,
		th.WithMiner(fixtures.TestMiners[0]),
		th.KeyFile(fixtures.KeyFilePaths()[0]),
		th.DefaultAddress(fixtures.TestAddresses[0])).Start()
	defer d1.ShutdownSuccess()

	d1.RunSuccess("mining", "start")

	setPrice := d1.RunSuccess("miner", "set-price", "62", "6", "--gas-price", "1", "--gas-limit", "300")
	assert.Contains(t, setPrice.ReadStdoutTrimNewlines(), fmt.Sprintf("Set price for miner %s to 62.", fixtures.TestMiners[0]))

	configuredPrice := d1.RunSuccess("config", "mining.storagePrice")

	assert.Equal(t, `"62"`, configuredPrice.ReadStdoutTrimNewlines())
}

func TestMinerCreateSuccess(t *testing.T) {
	tf.IntegrationTest(t)

	d1 := makeTestDaemonWithMinerAndStart(t)
	defer d1.ShutdownSuccess()
	d := th.NewDaemon(t, th.KeyFile(fixtures.KeyFilePaths()[2])).Start()
	defer d.ShutdownSuccess()
	d1.ConnectSuccess(d)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		miner := d.RunSuccess("miner", "create", "--from", fixtures.TestAddresses[2], "--gas-price", "1", "--gas-limit", "100", "200")
		addr, err := address.NewFromString(strings.Trim(miner.ReadStdout(), "\n"))
		assert.NoError(t, err)
		assert.NotEqual(t, addr, address.Undef)
		wg.Done()
	}()
	// ensure mining runs after the command in our goroutine
	d1.MineAndPropagate(time.Second, d)
	wg.Wait()
}

func TestMinerCreateChargesGas(t *testing.T) {
	tf.IntegrationTest(t)

	miningMinerOwnerAddr, err := address.NewFromString(fixtures.TestAddresses[0])
	require.NoError(t, err)

	d1 := makeTestDaemonWithMinerAndStart(t)
	defer d1.ShutdownSuccess()
	d := th.NewDaemon(t, th.KeyFile(fixtures.KeyFilePaths()[2])).Start()
	defer d.ShutdownSuccess()
	d1.ConnectSuccess(d)
	var wg sync.WaitGroup

	// make sure the FIL shows up in the MinerOwnerAccount
	startingBalance := queryBalance(t, d, miningMinerOwnerAddr)

	wg.Add(1)
	go func() {
		miner := d.RunSuccess("miner", "create", "--from", fixtures.TestAddresses[2], "--gas-price", "333", "--gas-limit", "100", "200")
		addr, err := address.NewFromString(strings.Trim(miner.ReadStdout(), "\n"))
		assert.NoError(t, err)
		assert.NotEqual(t, addr, address.Undef)
		wg.Done()
	}()
	// ensure mining runs after the command in our goroutine
	d1.MineAndPropagate(time.Second, d)
	wg.Wait()

	expectedBlockReward := consensus.NewDefaultBlockRewarder().BlockRewardAmount()
	expectedPrice := types.NewAttoFILFromFIL(333)
	expectedGasCost := big.NewInt(100)
	expectedBalance := expectedBlockReward.Add(expectedPrice.MulBigInt(expectedGasCost))
	newBalance := queryBalance(t, d, miningMinerOwnerAddr)
	assert.Equal(t, expectedBalance.String(), newBalance.Sub(startingBalance).String())
}

func queryBalance(t *testing.T, d *th.TestDaemon, actorAddr address.Address) types.AttoFIL {
	output := d.RunSuccess("actor", "ls", "--enc", "json")
	result := output.ReadStdoutTrimNewlines()
	for _, line := range bytes.Split([]byte(result), []byte{'\n'}) {
		var a commands.ActorView
		err := json.Unmarshal(line, &a)
		require.NoError(t, err)
		if a.Address == actorAddr.String() {
			return a.Balance
		}
	}
	t.Fail()
	return types.ZeroAttoFIL
}

func TestMinerOwner(t *testing.T) {
	tf.IntegrationTest(t)

	fi, err := ioutil.TempFile("", "gengentest")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = gengen.GenGenesisCar(testConfig, fi, 0); err != nil {
		t.Fatal(err)
	}

	_ = fi.Close()

	d := th.NewDaemon(t, th.GenesisFile(fi.Name())).Start()
	defer d.ShutdownSuccess()

	actorLsOutput := d.RunSuccess("actor", "ls")

	scanner := bufio.NewScanner(strings.NewReader(actorLsOutput.ReadStdout()))
	var addressStruct struct{ Address string }

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "MinerActor") {
			err = json.Unmarshal([]byte(line), &addressStruct)
			assert.NoError(t, err)
			break
		}
	}

	ownerOutput := d.RunSuccess("miner", "owner", addressStruct.Address)

	_, err = address.NewFromString(ownerOutput.ReadStdoutTrimNewlines())

	assert.NoError(t, err)
}

func TestMinerPower(t *testing.T) {
	tf.IntegrationTest(t)

	fi, err := ioutil.TempFile("", "gengentest")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = gengen.GenGenesisCar(testConfig, fi, 0); err != nil {
		t.Fatal(err)
	}

	_ = fi.Close()

	d := th.NewDaemon(t, th.GenesisFile(fi.Name())).Start()
	defer d.ShutdownSuccess()

	actorLsOutput := d.RunSuccess("actor", "ls")

	scanner := bufio.NewScanner(strings.NewReader(actorLsOutput.ReadStdout()))
	var addressStruct struct{ Address string }

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "MinerActor") {
			err = json.Unmarshal([]byte(line), &addressStruct)
			assert.NoError(t, err)
			break
		}
	}

	powerOutput := d.RunSuccess("miner", "power", addressStruct.Address)

	power := powerOutput.ReadStdoutTrimNewlines()

	assert.NoError(t, err)
	assert.Equal(t, "3072 / 6144", power)
}

var testConfig = &gengen.GenesisCfg{
	ProofsMode: types.TestProofsMode,
	Keys:       4,
	PreAlloc: []string{
		"10",
		"50",
	},
	Miners: []*gengen.CreateStorageMinerConfig{
		{
			Owner:               0,
			NumCommittedSectors: 3,
			SectorSize:          types.OneKiBSectorSize.Uint64(),
		},
		{
			Owner:               1,
			NumCommittedSectors: 3,
			SectorSize:          types.OneKiBSectorSize.Uint64(),
		},
	},
}
