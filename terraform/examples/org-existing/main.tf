provider "aws" { region = var.aws_region }
module "network" {
  source             = "../../modules/org-network"
  name               = var.name
  existing_vpc_id    = var.existing_vpc_id
  subnets            = var.subnets
  transit_gateway_id = var.transit_gateway_id
}
output "network" { value = module.network }

# Optional: creates a Launch Template using the approved existing network.
module "compute_profile" {
  count                 = var.compute_profile == null ? 0 : 1
  source                = "../../modules/managed-profile"
  name                  = var.name
  ami_id                = var.compute_profile.ami_id
  ami_checksum          = var.compute_profile.ami_checksum
  resource_class        = var.compute_profile.resource_class
  instance_type         = var.compute_profile.instance_type
  subnet_id             = module.network.subnet_ids[var.compute_profile.subnet_key]
  route_table_id        = module.network.route_table_ids[var.compute_profile.subnet_key]
  transit_gateway_id    = var.transit_gateway_id
  allowed_ipv4_cidrs    = var.compute_profile.allowed_ipv4_cidrs
  security_group_ids    = var.compute_profile.security_group_ids
  instance_profile_name = var.compute_profile.instance_profile_name
  config_parameter      = var.compute_profile.config_parameter
  approved_ami_owners   = var.compute_profile.approved_ami_owners
}

output "approved_profile" {
  value = try(module.compute_profile[0].approved_profile, null)
}
