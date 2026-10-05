provider "aws" { region = "ap-northeast-1" }
module "network" {
  source          = "../../modules/org-network"
  name            = "awsportal"
  existing_vpc_id = "vpc-0123456789abcdef0"
  subnets = {
    a = { id = "subnet-0123456789abcdef0", availability_zone = "ap-northeast-1a", route_table_id = "rtb-0123456789abcdef0" }
    b = { cidr = "10.42.2.0/24", availability_zone = "ap-northeast-1c", tgw_routes = ["10.0.0.0/8"] }
  }
  transit_gateway_id = "tgw-0123456789abcdef0"
}
output "network" { value = module.network }
