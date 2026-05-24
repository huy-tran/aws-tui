package aws

// CommonRegions seeds the region picker. The picker also accepts any
// region string the user types, so this list does not need to be exhaustive.
var CommonRegions = []string{
	"ap-southeast-2", // Sydney
	"ap-southeast-1", // Singapore
	"ap-southeast-4", // Melbourne
	"ap-northeast-1", // Tokyo
	"us-east-1",      // N. Virginia
	"us-east-2",      // Ohio
	"us-west-1",      // N. California
	"us-west-2",      // Oregon
	"eu-west-1",      // Ireland
	"eu-west-2",      // London
	"eu-central-1",   // Frankfurt
}
