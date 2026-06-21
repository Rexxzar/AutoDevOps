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

data "aws_vpc" "main" {
  id = "vpc-0123456789abcdef0"
}

locals {
  vpc_id         = data.aws_vpc.main.id
  vpc_cidr_block = data.aws_vpc.main.cidr_block
}

output "vpc_id" {
  value = local.vpc_id
}

data "aws_ami" "amazon_linux" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    # "al2023-ami-*-x86_64" also matches the "minimal" and "ecs-hvm" AL2023
    # variants, which omit the EC2 Instance Connect agent the Resource
    # Console relies on. Pin to the standard image: al2023-ami-2023.<ver>-kernel-<k>-x86_64.
    name   = "name"
    values = ["al2023-ami-2023.*-kernel-*-x86_64"]
  }
}

resource "aws_subnet" "ec2" {
  vpc_id                  = local.vpc_id
  cidr_block              = "10.0.1.0/24"
  availability_zone       = "us-east-1a"
  map_public_ip_on_launch = true

  tags = {
    Name = "demo-ec2"
  }
}

resource "aws_security_group" "ec2" {
  name        = "demo-ec2"
  description = "Allow SSH inbound and all outbound for demo"
  vpc_id      = local.vpc_id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "AutoDevOps app containers"
    from_port   = 8100
    to_port     = 8999
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "HTTP"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "demo-ec2"
  }
}

resource "aws_instance" "main" {
  ami                    = data.aws_ami.amazon_linux.id
  instance_type          = "t3.micro"
  subnet_id              = aws_subnet.ec2.id
  vpc_security_group_ids = [aws_security_group.ec2.id]

  # Installs Docker so the SSH deployer (internal/deploy cloud_vm target) can
  # "docker load"/"docker run" Paketo-built images without a separate
  # provisioning step. docker + nginx are installed first and on their own:
  # certbot is NOT in the AL2023 default repos, and "dnf install" is atomic, so
  # bundling it would fail the whole line and leave Docker uninstalled. certbot
  # is installed best-effort here (ProvisionVMDirect also installs it on demand
  # via a dnf/pip fallback if this misses).
  user_data = <<-EOF
    #!/bin/bash
    dnf install -y docker nginx
    systemctl enable --now docker nginx
    usermod -aG docker ec2-user
    dnf install -y certbot python3-certbot-nginx || pip3 install certbot certbot-nginx || true
  EOF

  tags = {
    Name = "demo"
  }
}

output "ec2_instance_id" {
  value = aws_instance.main.id
}

output "ec2_public_ip" {
  value = aws_instance.main.public_ip
}

output "ec2_private_ip" {
  value = aws_instance.main.private_ip
}
