package factory

import "context"

// AdmitLocalPlan validates the complete Plan against static local-executor
// capabilities without probing the invoking host, filesystem or runtime. It does
// not admit runtime resources or grant execution authority; RunLocal still owns
// those observations and the complete execution admission.
func AdmitLocalPlan(ctx context.Context, plan *Plan) error {
	return admitLocalPlanBase(ctx, plan)
}
