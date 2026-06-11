terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "us-east-1"
}

resource "aws_vpc" "main" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = {
    Name = "demo"
  }
}

locals {
  vpc_id         = aws_vpc.main.id
  vpc_cidr_block = aws_vpc.main.cidr_block
}

output "vpc_id" {
  value = local.vpc_id
}
