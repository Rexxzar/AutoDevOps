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

data "aws_ami" "amazon_linux" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }
}

resource "aws_subnet" "ec2" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.0.1.0/24"
  availability_zone = "us-east-1a"

  tags = {
    Name = "demo-ec2"
  }
}

resource "aws_security_group" "ec2" {
  name        = "demo-ec2"
  description = "Allow SSH inbound and all outbound for demo"
  vpc_id      = aws_vpc.main.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
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

  tags = {
    Name = "demo"
  }
}

resource "aws_subnet" "rds_0" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = "10.0.2.0/24"
  availability_zone = "us-east-1a"

  tags = {
    Name = "demo-rds-0"
  }
}

resource "aws_subnet" "rds_1" {
  vpc_id            = aws_vpc.main.id
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
  vpc_id      = aws_vpc.main.id

  ingress {
    description = "Database"
    from_port   = 5432
    to_port     = 5432
    protocol    = "tcp"
    cidr_blocks = [aws_vpc.main.cidr_block]
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
  engine_version              = "16.4"
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

resource "aws_subnet" "ecs_0" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = "10.0.4.0/24"
  availability_zone       = "us-east-1a"
  map_public_ip_on_launch = true

  tags = {
    Name = "demo-ecs-0"
  }
}

resource "aws_subnet" "ecs_1" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = "10.0.5.0/24"
  availability_zone       = "us-east-1b"
  map_public_ip_on_launch = true

  tags = {
    Name = "demo-ecs-1"
  }
}

resource "aws_internet_gateway" "ecs" {
  vpc_id = aws_vpc.main.id

  tags = {
    Name = "demo-ecs"
  }
}

resource "aws_route_table" "ecs" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.ecs.id
  }

  tags = {
    Name = "demo-ecs"
  }
}

resource "aws_route_table_association" "ecs_0" {
  subnet_id      = aws_subnet.ecs_0.id
  route_table_id = aws_route_table.ecs.id
}

resource "aws_route_table_association" "ecs_1" {
  subnet_id      = aws_subnet.ecs_1.id
  route_table_id = aws_route_table.ecs.id
}

resource "aws_security_group" "alb" {
  name        = "demo-alb"
  description = "Allow inbound HTTP to the demo load balancer"
  vpc_id      = aws_vpc.main.id

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
  vpc_id      = aws_vpc.main.id

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
  vpc_id      = aws_vpc.main.id
  target_type = "ip"

  health_check {
    path = "/"
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

resource "aws_ecs_task_definition" "main" {
  family                   = "demo"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "256"
  memory                   = "512"
  execution_role_arn       = aws_iam_role.ecs_execution.arn

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
  name            = "demo"
  cluster         = aws_ecs_cluster.main.id
  task_definition = aws_ecs_task_definition.main.arn
  desired_count   = 1
  launch_type     = "FARGATE"

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
