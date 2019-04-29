package types

// PoStProofPartitions represents the number of partitions used when creating a
// PoSt proof, and impacts the size of the proof.
type PoStProofPartitions uint64

const (
	// TestPoStPartitions is an opaque value signaling that an unknown number of
	// partitions were used when creating a PoSt proof in test mode.
	TestPoStPartitions = PoStProofPartitions(iota)

	// OnePoStPartition indicates that a single partition was used to create a PoSt proof.
	OnePoStPartition
)
