# Override files patch a block declared elsewhere; they never introduce an
# address of their own. Indexing this would move the annotation off the real
# declaration in main.tf and onto the patch.
resource "aws_iam_role" "deploy" {
  description = "patched"
}
