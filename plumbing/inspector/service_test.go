package inspector

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/filecoin-project/go-filecoin/config"
	"github.com/filecoin-project/go-filecoin/repo"
	tf "github.com/filecoin-project/go-filecoin/testhelpers/testflags"
)

func TestRuntime(t *testing.T) {
	tf.UnitTest(t)

	mr := repo.NewInMemoryRepo()
	g := New(mr)
	rt := g.Runtime()

	assert.Equal(t, runtime.GOOS, rt.OS)
	assert.Equal(t, runtime.GOARCH, rt.Arch)
	assert.Equal(t, runtime.Version(), rt.Version)
	assert.Equal(t, runtime.Compiler, rt.Compiler)
	assert.Equal(t, runtime.NumCPU(), rt.NumProc)
	assert.Equal(t, runtime.GOMAXPROCS(0), rt.GoMaxProcs)
	assert.Equal(t, runtime.NumGoroutine(), rt.NumGoRoutines)
	assert.Equal(t, runtime.NumCgoCall(), rt.NumCGoCalls)
}

func TestDisk(t *testing.T) {
	tf.UnitTest(t)

	mr := repo.NewInMemoryRepo()
	g := New(mr)
	d, err := g.Disk()

	assert.NoError(t, err)
	assert.Equal(t, uint64(0), d.Free)
	assert.Equal(t, uint64(0), d.Total)
	assert.Equal(t, "0", d.FSType)
}

func TestMemory(t *testing.T) {
	tf.UnitTest(t)

	mr := repo.NewInMemoryRepo()
	g := New(mr)

	_, err := g.Memory()
	assert.NoError(t, err)
}

func TestConfig(t *testing.T) {
	tf.UnitTest(t)

	mr := repo.NewInMemoryRepo()
	g := New(mr)
	c := g.Config()
	assert.Equal(t, config.NewDefaultConfig(), c)
}
