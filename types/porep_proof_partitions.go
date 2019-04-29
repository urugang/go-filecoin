package types

// PoRepProofPartitions represents the number of partitions used when creating a
// PoRep proof, and impacts the size of the proof.
type PoRepProofPartitions uint64

const (
	// TestPoRepProofPartitions is an opaque value signaling that an unknown number
	// of partitions were used when creating a PoRep proof in test mode.
	TestPoRepProofPartitions = PoRepProofPartitions(iota)

	// TwoPoRepPartitions indicates that two partitions were used to create a PoRep proof.
	TwoPoRepPartitions
)
