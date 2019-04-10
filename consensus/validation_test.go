package consensus_test

import (
	"context"
	"fmt"
	"github.com/filecoin-project/go-filecoin/actor/builtin"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/ipfs/go-hamt-ipld"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"

	"github.com/filecoin-project/go-filecoin/actor"
	"github.com/filecoin-project/go-filecoin/actor/builtin/account"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/consensus"
	"github.com/filecoin-project/go-filecoin/types"
)

var seed = types.GenerateKeyInfoSeed()
var keys = types.MustGenerateKeyInfo(2, seed)
var signer = types.NewMockSigner(keys)
var addresses = make([]address.Address, len(keys))

func init() {
	for i, k := range keys {
		addr, _ := k.Address()
		addresses[i] = addr
	}
}

func TestMessageValidator(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	alice := addresses[0]
	bob := addresses[1]
	actor := newActor(t, 1000, 100)

	validator := consensus.NewDefaultMessageValidator()
	ctx := context.Background()

	t.Run("valid", func(t *testing.T) {
		msg := newMessage(t, alice, bob, 100, 5, 0, 0)
		assert.NoError(validator.Validate(ctx, msg, actor))
	})

	t.Run("invalid signature fails", func(t *testing.T) {
		msg := newMessage(t, alice, bob, 100, 5, 0, 0)
		msg.Signature = []byte{}
		assert.Errorf(validator.Validate(ctx, msg, actor), "signature")

	})

	t.Run("self send fails", func(t *testing.T) {
		msg := newMessage(t, alice, alice, 100, 5, 0, 0)
		assert.Errorf(validator.Validate(ctx, msg, actor), "self")
	})

	t.Run("non-account actor fails", func(t *testing.T) {
		badActor := newActor(t, 1000, 100)
		badActor.Code = types.SomeCid()
		msg := newMessage(t, alice, bob, 100, 5, 0, 0)
		assert.Errorf(validator.Validate(ctx, msg, badActor), "account")
	})

	t.Run("negative value fails", func(t *testing.T) {
		msg := newMessage(t, alice, alice, 100, -5, 0, 0)
		assert.Errorf(validator.Validate(ctx, msg, actor), "negative")
	})

	t.Run("block gas limit fails", func(t *testing.T) {
		msg := newMessage(t, alice, bob, 100, 5, 0, uint64(types.BlockGasLimit)+1)
		assert.Errorf(validator.Validate(ctx, msg, actor), "block limit")
	})

	t.Run("can't cover value", func(t *testing.T) {
		msg := newMessage(t, alice, bob, 100, 2000, 0, 0) // lots of value
		assert.Errorf(validator.Validate(ctx, msg, actor), "funds")

		msg = newMessage(t, alice, bob, 100, 5, 10^18, 200) // lots of expensive gas
		assert.Errorf(validator.Validate(ctx, msg, actor), "funds")
	})

	t.Run("low nonce", func(t *testing.T) {
		msg := newMessage(t, alice, bob, 99, 5, 0, 0)
		assert.Errorf(validator.Validate(ctx, msg, actor), "too low")
	})

	t.Run("high nonce", func(t *testing.T) {
		msg := newMessage(t, alice, bob, 101, 5, 0, 0)
		assert.Errorf(validator.Validate(ctx, msg, actor), "too high")
	})
}

func TestOutboundMessageValidator(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)

	alice := addresses[0]
	bob := addresses[1]
	actor := newActor(t, 1000, 100)

	validator := consensus.NewOutboundMessageValidator()
	ctx := context.Background()

	t.Run("allows high nonce", func(t *testing.T) {
		msg := newMessage(t, alice, bob, 100, 5, 0, 0)
		assert.NoError(validator.Validate(ctx, msg, actor))
		msg = newMessage(t, alice, bob, 101, 5, 0, 0)
		assert.NoError(validator.Validate(ctx, msg, actor))
	})
}

func TestIngestionValidator(t *testing.T) {
	t.Parallel()

	alice := addresses[0]
	bob := addresses[1]
	act := newActor(t, 1000, 53)
	api := NewMockIngestionValidatorAPI()
	api.ActorAddr = alice
	api.Actor = act

	validator := consensus.NewIngestionValidator(api)
	ctx := context.Background()

	t.Run("Validates extreme nonce gaps", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)

		msg := newMessage(t, alice, bob, 100, 5, 0, 0)
		assert.NoError(validator.Validate(ctx, msg))

		highNonce := uint64(act.Nonce + consensus.MaxNonceGap + 10)
		msg = newMessage(t, alice, bob, highNonce, 5, 0, 0)
		err := validator.Validate(ctx, msg)
		require.Error(err)
		assert.Contains(err.Error(), "too much greater than actor nonce")
	})

	t.Run("Actor not found is not an error", func(t *testing.T) {
		assert := assert.New(t)

		msg := newMessage(t, bob, alice, 0, 0, 0, 0)
		assert.NoError(validator.Validate(ctx, msg))
	})
}

func newActor(t *testing.T, balanceAF int, nonce uint64) *actor.Actor {
	actor, err := account.NewActor(attoFil(balanceAF))
	require.NoError(t, err)
	actor.Nonce = types.Uint64(nonce)
	return actor
}

func newMessage(t *testing.T, from, to address.Address, nonce uint64, valueAF int,
	gasPrice int64, gasLimit uint64) *types.SignedMessage {
	val, ok := types.NewAttoFILFromString(fmt.Sprintf("%d", valueAF), 10)
	require.True(t, ok, "invalid attofil")
	msg := types.NewMessage(
		from,
		to,
		nonce,
		val,
		"method",
		[]byte("params"),
	)
	signed, err := types.NewSignedMessage(*msg, signer, types.NewGasPrice(gasPrice), types.NewGasUnits(gasLimit))
	require.NoError(t, err)
	return signed
}

func attoFil(v int) *types.AttoFIL {
	val, _ := types.NewAttoFILFromString(fmt.Sprintf("%d", v), 10)
	return val
}

// MockIngestionValidatorAPI provides a latest state
type MockIngestionValidatorAPI struct {
	ActorAddr address.Address
	Actor     *actor.Actor
}

// NewMockIngestionValidatorAPI creates a new MockIngestionValidatorAPI.
func NewMockIngestionValidatorAPI() *MockIngestionValidatorAPI {
	return &MockIngestionValidatorAPI{Actor: &actor.Actor{}}
}

// LatestState will be a state tree that only contains the test actor
func (api *MockIngestionValidatorAPI) LatestState(ctx context.Context) (state.Tree, error) {
	cst := hamt.NewCborStore()
	st := state.NewEmptyStateTreeWithActors(cst, builtin.Actors)
	err := st.SetActor(ctx, api.ActorAddr, api.Actor)
	if err != nil {
		return nil, err
	}
	return st, nil
}
