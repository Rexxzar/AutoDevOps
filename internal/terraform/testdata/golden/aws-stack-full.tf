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

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id

  tags = {
    Name = "demo"
  }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }

  tags = {
    Name = "demo-public"
  }
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

resource "aws_route_table_association" "ec2" {
  subnet_id      = aws_subnet.ec2.id
  route_table_id = aws_route_table.public.id
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

resource "aws_subnet" "rds_0" {
  vpc_id                  = local.vpc_id
  cidr_block              = "10.0.2.0/24"
  availability_zone       = "us-east-1a"
  map_public_ip_on_launch = true

  tags = {
    Name = "demo-rds-0"
  }
}

resource "aws_subnet" "rds_1" {
  vpc_id                  = local.vpc_id
  cidr_block              = "10.0.3.0/24"
  availability_zone       = "us-east-1b"
  map_public_ip_on_launch = true

  tags = {
    Name = "demo-rds-1"
  }
}


resource "aws_route_table_association" "rds_0" {
  subnet_id      = aws_subnet.rds_0.id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table_association" "rds_1" {
  subnet_id      = aws_subnet.rds_1.id
  route_table_id = aws_route_table.public.id
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

  # Open on the DB port so workloads in other VPCs (a separate ECS service or
  # EC2 VM provisioned as its own resource/stack) can reach this database — the
  # AWS-managed master password (Secrets Manager) is the security boundary,
  # mirroring the Azure Postgres "public access + AllowAzureServices" posture.
  ingress {
    description = "Database"
    from_port   = 5432
    to_port     = 5432
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
  publicly_accessible         = true
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

resource "aws_subnet" "ecs_0" {
  vpc_id                  = local.vpc_id
  cidr_block              = "10.0.4.0/24"
  availability_zone       = "us-east-1a"
  map_public_ip_on_launch = true

  tags = {
    Name = "demo-ecs-0"
  }
}

resource "aws_subnet" "ecs_1" {
  vpc_id                  = local.vpc_id
  cidr_block              = "10.0.5.0/24"
  availability_zone       = "us-east-1b"
  map_public_ip_on_launch = true

  tags = {
    Name = "demo-ecs-1"
  }
}


resource "aws_route_table_association" "ecs_0" {
  subnet_id      = aws_subnet.ecs_0.id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table_association" "ecs_1" {
  subnet_id      = aws_subnet.ecs_1.id
  route_table_id = aws_route_table.public.id
}

resource "aws_security_group" "alb" {
  name        = "demo-alb"
  description = "Allow inbound HTTP to the demo load balancer"
  vpc_id      = local.vpc_id

  ingress {
    description = "HTTP"
    from_port   = 80
    to_port     = 80
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
    Name = "demo-alb"
  }
}

resource "aws_security_group" "ecs_task" {
  name        = "demo-ecs-task"
  description = "Allow inbound traffic from the load balancer to demo tasks"
  vpc_id      = local.vpc_id

  ingress {
    description     = "From ALB"
    from_port       = 80
    to_port         = 80
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "demo-ecs-task"
  }
}

resource "aws_lb" "main" {
  name               = "demo-alb"
  load_balancer_type = "application"
  internal           = false
  security_groups    = [aws_security_group.alb.id]
  subnets            = [aws_subnet.ecs_0.id, aws_subnet.ecs_1.id, ]

  tags = {
    Name = "demo-alb"
  }
}

resource "aws_lb_target_group" "main" {
  name        = "demo-tg"
  port        = 80
  protocol    = "HTTP"
  vpc_id      = local.vpc_id
  target_type = "ip"

  # Accept any non-5xx response (including 404) as healthy — this is a liveness
  # check ("is the container serving HTTP"), not a correctness check. Backend
  # apps often only serve "/api/*" and return 404 on "/", which fails both the
  # default 200-only matcher AND a 200-399 matcher, leaving the ALB at 503. The
  # tightened interval + threshold bring a new task into service in ~30s
  # (2 x 15s) instead of the ~150s default (5 x 30s).
  health_check {
    path                = "/"
    matcher             = "200-499"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }

  tags = {
    Name = "demo-tg"
  }
}

resource "aws_lb_listener" "main" {
  load_balancer_arn = aws_lb.main.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.main.arn
  }
}

resource "aws_ecs_cluster" "main" {
  name = "demo"

  tags = {
    Name = "demo"
  }
}

resource "aws_cloudwatch_log_group" "ecs" {
  name              = "/ecs/demo"
  retention_in_days = 7
}

resource "aws_iam_role" "ecs_execution" {
  name = "demo-ecs-execution"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Action    = "sts:AssumeRole"
        Effect    = "Allow"
        Principal = { Service = "ecs-tasks.amazonaws.com" }
      }
    ]
  })

  tags = {
    Name = "demo-ecs-execution"
  }
}

resource "aws_iam_role_policy_attachment" "ecs_execution" {
  role       = aws_iam_role.ecs_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role" "ecs_task" {
  name = "demo-ecs-task"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Action    = "sts:AssumeRole"
        Effect    = "Allow"
        Principal = { Service = "ecs-tasks.amazonaws.com" }
      }
    ]
  })

  tags = {
    Name = "demo-ecs-task"
  }
}

resource "aws_iam_role_policy" "ecs_task_exec" {
  name = "demo-ecs-exec"
  role = aws_iam_role.ecs_task.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = [
          "ssmmessages:CreateControlChannel",
          "ssmmessages:CreateDataChannel",
          "ssmmessages:OpenControlChannel",
          "ssmmessages:OpenDataChannel",
          "ssm:StartSession",
          "ecs:ExecuteCommand"
        ]
        Resource = "*"
      }
    ]
  })
}

resource "aws_ecs_task_definition" "main" {
  family                   = "demo"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.ecs_execution.arn
  task_role_arn            = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name      = "demo"
      image     = "public.ecr.aws/nginx/nginx:1.27"
      essential = true
      portMappings = [
        {
          containerPort = 80
          protocol      = "tcp"
        }
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.ecs.name
          "awslogs-region"        = "us-east-1"
          "awslogs-stream-prefix" = "ecs"
        }
      }
    }
  ])

  tags = {
    Name = "demo"
  }
}

resource "aws_ecs_service" "main" {
  name                   = "demo"
  cluster                = aws_ecs_cluster.main.id
  task_definition        = aws_ecs_task_definition.main.arn
  desired_count          = 1
  launch_type            = "FARGATE"
  enable_execute_command = true

  # Give a new task 120s to boot (pull image, run DB migrations, warm up)
  # before the ALB health check starts counting against it — otherwise ECS
  # kills the task mid-startup ("failed ELB health checks", SIGKILL/exit 137)
  # and restarts it in a loop before it ever gets a chance to come up.
  health_check_grace_period_seconds = 120

  # Keep the whole fleet healthy during a rolling deploy (min 100% stays up,
  # new tasks come up alongside) so a redeploy never drops to zero healthy
  # targets — with desired_count >= 2 this is genuinely zero-downtime.
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200

  # If a new task version fails its health checks, abort and roll back to the
  # last working version instead of crash-looping forever. The old version
  # keeps serving throughout, so a bad deploy causes no outage.
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = [aws_subnet.ecs_0.id, aws_subnet.ecs_1.id, ]
    security_groups  = [aws_security_group.ecs_task.id]
    assign_public_ip = true
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.main.arn
    container_name   = "demo"
    container_port   = 80
  }

  depends_on = [aws_lb_listener.main]

  tags = {
    Name = "demo"
  }
}

output "ecs_alb_dns_name" {
  value = aws_lb.main.dns_name
}

output "ecs_cluster_name" {
  value = aws_ecs_cluster.main.name
}

resource "aws_ecr_repository" "main" {
  name                 = lower("demo")
  image_tag_mutability = "MUTABLE"

  # Allow destroy even when the repo still holds pushed images — without this,
  # terraform destroy fails with RepositoryNotEmptyException and the resource
  # can never be torn down through AutoDevOps once an app has pushed to it.
  force_delete = true

  image_scanning_configuration {
    scan_on_push = true
  }

  tags = {
    Name = "demo"
  }
}

output "ecr_repository_url" {
  value = aws_ecr_repository.main.repository_url
}
