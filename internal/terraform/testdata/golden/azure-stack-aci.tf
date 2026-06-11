terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
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

resource "azurerm_container_group" "main" {
  name                = "demo-aci"
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  os_type             = "Linux"
  ip_address_type     = "Public"
  dns_name_label      = "demo"

  container {
    name   = "demo"
    image  = "mcr.microsoft.com/azuredocs/aci-helloworld:latest"
    cpu    = 1
    memory = 1.5

    ports {
      port     = 80
      protocol = "TCP"
    }
  }

  tags = {
    Name = "demo"
  }
}

output "aci_ip_address" {
  value = azurerm_container_group.main.ip_address
}

output "aci_fqdn" {
  value = azurerm_container_group.main.fqdn
}
