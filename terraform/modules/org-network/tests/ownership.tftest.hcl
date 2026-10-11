mock_provider "aws" {
  mock_data "aws_vpc" { defaults = { id = "vpc-test" } }
  mock_data "aws_subnet" { defaults = { id = "subnet-test", vpc_id = "vpc-test", availability_zone = "ap-northeast-1a", cidr_block = "10.42.1.0/24" } }
}
run "provided_network_is_read_only" {
  command = plan
  variables {
    name               = "test"
    existing_vpc_id    = "vpc-test"
    subnets            = { a = { id = "subnet-test", availability_zone = "ap-northeast-1a", route_table_id = "rtb-test" } }
    transit_gateway_id = "tgw-test"
  }
  assert {
    condition     = length(aws_vpc.private) == 0 && length(aws_subnet.private) == 0 && length(aws_route_table.private) == 0 && length(aws_route.tgw) == 0 && length(aws_route_table_association.private) == 0 && length(aws_ec2_transit_gateway_vpc_attachment.private) == 0
    error_message = "Provided network must not be managed or re-associated."
  }
}
run "new_private_network_has_no_internet" {
  command = plan
  variables {
    name     = "test"
    vpc_cidr = "10.42.0.0/16"
    subnets  = { a = { cidr = "10.42.1.0/24", availability_zone = "ap-northeast-1a" } }
  }
  assert {
    condition     = length(aws_vpc.private) == 1 && length(aws_subnet.private) == 1 && length(aws_route.tgw) == 0
    error_message = "New private deployment must not invent Internet routes."
  }
}
run "mixed_network_only_owns_new_part" {
  command = plan
  variables {
    name               = "test"
    existing_vpc_id    = "vpc-test"
    subnets            = { a = { id = "subnet-test", availability_zone = "ap-northeast-1a", route_table_id = "rtb-test" }, b = { cidr = "10.42.2.0/24", availability_zone = "ap-northeast-1c", tgw_routes = ["10.0.0.0/8"] } }
    transit_gateway_id = "tgw-test"
  }
  assert {
    condition     = length(aws_vpc.private) == 0 && length(aws_subnet.private) == 1 && length(aws_route_table.private) == 1 && length(aws_route.tgw) == 1 && length(aws_route_table_association.private) == 1
    error_message = "Only new subnet/routes may be managed in mixed mode."
  }
}
