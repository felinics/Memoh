package native

// maxRefusedBatches is how many consecutive entirely-refused tool batches a
// run tolerates before it ends as a tool loop. The executor refuses a call
// whose arguments are not a JSON document or that names a tool the model was
// not offered; such calls are answered with error results and never reach a
// wrapped Execute, so the tool-loop guard does not see them. This bound holds
// whether or not loop detection is on, the way the previous SDK loop ended a
// turn whose batch had nothing to execute.
const maxRefusedBatches = 3

// refusedBatches counts consecutive steps whose whole tool batch the executor
// refused.
type refusedBatches int

// note records whether the step's batch was refused entirely and reports
// when the run has hit maxRefusedBatches in a row. A batch with any call
// that ran resets the count.
func (c *refusedBatches) note(allRefused bool) bool {
	if !allRefused {
		*c = 0
		return false
	}
	*c++
	return int(*c) >= maxRefusedBatches
}

// batchRefused reports whether every call of a batch was refused.
func batchRefused(refused, calls int) bool {
	return refused > 0 && refused == calls
}
