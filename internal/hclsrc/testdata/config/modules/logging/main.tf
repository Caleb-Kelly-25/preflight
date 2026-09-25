resource "aws_cloudwatch_log_group" "app" {
  name = "app"
}

module "inner" {
  source = "./inner"
}
