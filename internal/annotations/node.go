// SPDX-FileCopyrightText: Copyright (C) SchedMD LLC.
// SPDX-License-Identifier: Apache-2.0

package annotations

const (
	// NodeWeight can be used to set to an int32 that represents the weight of
	// scheduling a NodeSet Pod compared to other Nodes.
	// Note that this is honored on a best-effort basis, and so it does not
	// offer guarantees on Node scheduling order.
	NodeWeight = "slinky.slurm.net/node-weight"
	NodeCordon = "slinky.slurm.net/node-cordon"
)
