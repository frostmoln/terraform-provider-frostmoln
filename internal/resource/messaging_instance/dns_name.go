package messaging_instance

import (
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// errCodeDNSNameTaken is messaging's synchronous 409 on create when another instance in
// the same VPC already holds the DNS label of the requested name, including an instance
// renamed since (the label is fixed at create). Nothing was created and no provisioning
// started (MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN, Phase 4a).
const errCodeDNSNameTaken = "INSTANCE_DNS_NAME_TAKEN"

func isDNSNameTaken(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Code == errCodeDNSNameTaken
}

// addDNSNameTakenError puts the refusal on `name`, the attribute the practitioner changes.
func addDNSNameTakenError(diags *diag.Diagnostics, name string, err error) {
	diags.AddAttributeError(path.Root("name"),
		"Messaging Instance DNS Name Already Used In The VPC",
		fmt.Sprintf("Another messaging instance in this VPC already holds the DNS name of %q. Instance names "+
			"are DNS names, unique per VPC, and an instance keeps its DNS name when it is renamed, so a renamed "+
			"instance can still hold it. Nothing was created and nothing has been recorded in Terraform state.\n\n"+
			"Choose another `name`. With `create_before_destroy`, the replacement needs a name different from "+
			"the instance it replaces.\n\nThe platform said: %s", name, err))
}
