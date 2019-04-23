package types

// PoStProofPartitions TODO(laser)
type PoStProofPartitions uint64

const (
	// TestPoStPartitions TODO(laser)
	TestPoStPartitions = PoStProofPartitions(iota)
	// OnePoStPartition TODO(laser)
	OnePoStPartition
)
