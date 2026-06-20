terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}

provider "azurerm" {
  features {}
}

resource "azurerm_resource_group" "main" {
  name     = "demo-rg"
  location = "uksouth"

  tags = {
    Name = "demo"
  }
}

output "resource_group_name" {
  value = azurerm_resource_group.main.name
}

resource "azurerm_virtual_network" "main" {
  name                = "demo-vnet"
  address_space       = ["10.0.0.0/16"]
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name

  tags = {
    Name = "demo"
  }
}

locals {
  vnet_name           = azurerm_virtual_network.main.name
  vnet_resource_group = azurerm_resource_group.main.name
}

data "azurerm_client_config" "current" {}

resource "random_password" "db" {
  length  = 32
  special = true
}

resource "azurerm_postgresql_flexible_server" "main" {
  name                = "demo-db"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  version             = "16"
  sku_name            = "B_Standard_B1ms"
  storage_mb          = 32768

  administrator_login    = "autodevopsadmin"
  administrator_password = random_password.db.result

  public_network_access_enabled = true

  authentication {
    active_directory_auth_enabled = true
    password_auth_enabled         = true
    tenant_id                     = data.azurerm_client_config.current.tenant_id
  }

  tags = {
    Name = "demo-db"
  }
}

resource "azurerm_postgresql_flexible_server_active_directory_administrator" "main" {
  server_name         = azurerm_postgresql_flexible_server.main.name
  resource_group_name = azurerm_resource_group.main.name
  tenant_id           = data.azurerm_client_config.current.tenant_id
  object_id           = "00000000-0000-0000-0000-000000000000"
  principal_name      = "alice@example.com"
  principal_type      = "User"
}

resource "azurerm_postgresql_flexible_server_firewall_rule" "azure_services" {
  name             = "AllowAzureServices"
  server_id        = azurerm_postgresql_flexible_server.main.id
  start_ip_address = "0.0.0.0"
  end_ip_address   = "0.0.0.0"
}

output "db_fqdn" {
  value = azurerm_postgresql_flexible_server.main.fqdn
}

output "db_admin_username" {
  value = azurerm_postgresql_flexible_server.main.administrator_login
}

output "db_admin_password" {
  value     = random_password.db.result
  sensitive = true
}
