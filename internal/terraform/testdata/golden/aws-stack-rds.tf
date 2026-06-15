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

resource "aws_subnet" "rds_0" {
  vpc_id            = local.vpc_id
  cidr_block        = "10.0.2.0/24"
  availability_zone = "us-east-1a"

  tags = {
    Name = "demo-rds-0"
  }
}

resource "aws_subnet" "rds_1" {
  vpc_id            = local.vpc_id
  cidr_block        = "10.0.3.0/24"
  availability_zone = "us-east-1b"

  tags = {
    Name = "demo-rds-1"
  }
}

resource "aws_db_subnet_group" "rds" {
  name       = "demo-rds"
  subnet_ids = [aws_subnet.rds_0.id, aws_subnet.rds_1.id, ]

  tags = {
    Name = "demo-rds"
  }
}

resource "aws_security_group" "rds" {
  name        = "demo-rds"
  description = "Allow database access from within the VPC for demo"
  vpc_id      = local.vpc_id

  ingress {
    description = "Database"
    from_port   = 5432
    to_port     = 5432
    protocol    = "tcp"
    cidr_blocks = [local.vpc_cidr_block]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "demo-rds"
  }
}

resource "aws_db_instance" "main" {
  identifier                  = "demo-db"
  engine                      = "postgres"
  engine_version              = "16"
  instance_class              = "db.t3.micro"
  allocated_storage           = 20
  username                    = "appadmin"
  manage_master_user_password = true
  port                        = 5432
  db_subnet_group_name        = aws_db_subnet_group.rds.name
  vpc_security_group_ids      = [aws_security_group.rds.id]
  skip_final_snapshot         = true

  tags = {
    Name = "demo-db"
  }
}

output "rds_endpoint" {
  value = aws_db_instance.main.address
}

output "rds_port" {
  value = aws_db_instance.main.port
}

output "rds_username" {
  value = aws_db_instance.main.username
}

output "rds_engine" {
  value = aws_db_instance.main.engine
}

output "rds_master_user_secret_arn" {
  value = aws_db_instance.main.master_user_secret[0].secret_arn
}

output "rds_security_group_id" {
  value = aws_security_group.rds.id
}
