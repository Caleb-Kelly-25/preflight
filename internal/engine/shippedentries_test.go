package engine_test

import (
	"slices"
	"testing"

	"github.com/Caleb-Kelly-25/preflight/internal/engine"
	"github.com/Caleb-Kelly-25/preflight/internal/mapping"
	"github.com/Caleb-Kelly-25/preflight/mappings"
)

// shippedDB loads the real mapping database, so these tests exercise the entries
// actually shipped rather than a fixture that agrees with them.
func shippedDB(t *testing.T) *mapping.Database {
	t.Helper()
	db, err := mapping.Load(mappings.FS)
	if err != nil {
		t.Fatalf("loading the shipped database: %v", err)
	}
	return db
}

// TestListenerCreateIsAskedAgainstTheLoadBalancer pins the one structural claim
// in mappings/elb.yaml that is not a plain action list.
//
// elasticloadbalancing:CreateListener is authorised against the LOAD BALANCER,
// not the listener — the listener does not exist yet and has no ARN to authorise
// against. So aws_lb_listener's `operations.create` deliberately carries no
// action on the listener itself and the create lives under `references`.
//
// That arrangement only works if the engine still resolves references for an
// operation whose own action list is empty. It does, and this test is what says
// so: without it, the entry's whole create path could silently vanish and the
// only symptom would be a listener reported as needing no create permission —
// a false pass, and exactly the kind produced by a mapping that looks right.
func TestListenerCreateIsAskedAgainstTheLoadBalancer(t *testing.T) {
	const lbARN = "arn:aws:elasticloadbalancing:us-east-1:111122223333:loadbalancer/app/my-lb/abc123def456"

	p := planFromJSON(t, `{
      "format_version": "1.2",
      "terraform_version": "1.9.0",
      "resource_changes": [
        {"address":"aws_lb_listener.web","mode":"managed","type":"aws_lb_listener","name":"web",
         "provider_name":"registry.terraform.io/hashicorp/aws",
         "change":{"actions":["create"],"before":null,
                   "after":{"load_balancer_arn":"`+lbARN+`","port":80,"protocol":"HTTP"},
                   "after_unknown":{"arn":true}}}
      ]}`)

	sim := &fakeSimulator{}
	analyzePlan(t, p, engine.Options{Database: shippedDB(t), Simulator: sim})

	if len(sim.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(sim.requests))
	}
	byARN := map[string][]string{}
	for _, item := range sim.requests[0].Items {
		byARN[item.ResourceARN] = append(byARN[item.ResourceARN], item.Actions...)
	}

	if !slices.Contains(byARN[lbARN], "elasticloadbalancing:CreateListener") {
		t.Errorf("CreateListener was not asked against the load balancer %q; asked: %v", lbARN, byARN)
	}

	// And it must not ALSO be asked against the listener. The listener's own ARN
	// is unknown at create, so it degrades to "*" — asking there would be asking
	// AWS a question its model has no answer to, and an ARN-scoped policy naming
	// the load balancer would come back denied.
	for arn, actions := range byARN {
		if arn == lbARN {
			continue
		}
		if slices.Contains(actions, "elasticloadbalancing:CreateListener") {
			t.Errorf("CreateListener was also asked against %q; it belongs only on the load balancer", arn)
		}
	}
}

// TestRoute53ARNsCarryNoRegionOrAccount pins the Route 53 exception. Its ARNs are
// global — `arn:aws:route53:::hostedzone/Z123`, with region and account EMPTY —
// and every other entry in the database templates the caller's region and
// account in. If that ever leaked in here, the ARN would match nothing and every
// simulation against it would come back denied, which is indistinguishable from
// a real permission gap.
func TestRoute53ARNsCarryNoRegionOrAccount(t *testing.T) {
	const zoneID = "Z1D633PJN98FT9"

	// A record in an EXISTING zone: zone_id is known, so the ARN must resolve
	// exactly rather than degrading.
	p := planFromJSON(t, `{
      "format_version": "1.2",
      "terraform_version": "1.9.0",
      "resource_changes": [
        {"address":"aws_route53_record.www","mode":"managed","type":"aws_route53_record","name":"www",
         "provider_name":"registry.terraform.io/hashicorp/aws",
         "change":{"actions":["create"],"before":null,
                   "after":{"zone_id":"`+zoneID+`","name":"www.example.com","type":"A"}}}
      ]}`)

	sim := &fakeSimulator{}
	analyzePlan(t, p, engine.Options{Database: shippedDB(t), Simulator: sim, Region: "eu-west-2"})

	if len(sim.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(sim.requests))
	}

	want := "arn:aws:route53:::hostedzone/" + zoneID
	var found bool
	for _, item := range sim.requests[0].Items {
		if item.ResourceARN == want {
			found = true
		}
		// The region was deliberately set to eu-west-2 above: if the entry ever
		// starts templating it, this catches it by name rather than by shape.
		if item.ResourceARN != want && item.ResourceARN != "*" {
			t.Errorf("unexpected ARN %q; Route 53 ARNs carry no region or account", item.ResourceARN)
		}
	}
	if !found {
		var got []string
		for _, item := range sim.requests[0].Items {
			got = append(got, item.ResourceARN)
		}
		t.Errorf("record was not asked against the containing hosted zone %q; asked: %v", want, got)
	}
}

// TestKMSAliasIsAskedAgainstTheTargetKey pins the other half of a two-resource
// authorisation. Every alias call is authorised against BOTH the alias and the
// key it points at, so an entry naming only the alias would under-report on
// every single operation.
//
// It also covers `arn_or_name` on `target_key_id`, which legitimately holds
// either a bare key id or a full key ARN. Both spellings are common in real
// configurations, and the two are NOT interchangeable to the engine — which is
// the second thing this test pins:
//
//   - a bare id is templated against the CALLER's account and region, because
//     that is the only account it could refer to;
//   - a full ARN is used exactly as written, carrying its own account and
//     region, so a key in another account is not silently rewritten into one in
//     the caller's.
//
// Getting that backwards would build a confidently wrong ARN, which is the
// dangerous direction: a policy scoped to the real key would be evaluated
// against a key that does not exist.
func TestKMSAliasIsAskedAgainstTheTargetKey(t *testing.T) {
	// The fake identity is arn:aws:iam::123456789012:user/deployer, so a bare id
	// must come back templated into THAT account.
	for name, tc := range map[string]struct{ targetKeyID, wantKeyARN string }{
		"bare key id is templated against the caller": {
			targetKeyID: "1234abcd-12ab-34cd-56ef-1234567890ab",
			wantKeyARN:  "arn:aws:kms:us-east-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		},
		"a full arn keeps its own account and region": {
			targetKeyID: "arn:aws:kms:eu-west-2:999988887777:key/1234abcd-12ab-34cd-56ef-1234567890ab",
			wantKeyARN:  "arn:aws:kms:eu-west-2:999988887777:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := planFromJSON(t, `{
              "format_version": "1.2",
              "terraform_version": "1.9.0",
              "resource_changes": [
                {"address":"aws_kms_alias.a","mode":"managed","type":"aws_kms_alias","name":"a",
                 "provider_name":"registry.terraform.io/hashicorp/aws",
                 "change":{"actions":["create"],"before":null,
                           "after":{"name":"alias/my-key","target_key_id":"`+tc.targetKeyID+`"},
                           "after_unknown":{"arn":true}}}
              ]}`)

			sim := &fakeSimulator{}
			analyzePlan(t, p, engine.Options{Database: shippedDB(t), Simulator: sim})

			if len(sim.requests) != 1 {
				t.Fatalf("expected one request, got %d", len(sim.requests))
			}
			var asked []string
			for _, item := range sim.requests[0].Items {
				if item.ResourceARN == tc.wantKeyARN && slices.Contains(item.Actions, "kms:CreateAlias") {
					return // the key half was asked for
				}
				asked = append(asked, item.ResourceARN)
			}
			t.Errorf("kms:CreateAlias was not asked against the target key %q; asked: %v",
				tc.wantKeyARN, asked)
		})
	}
}
