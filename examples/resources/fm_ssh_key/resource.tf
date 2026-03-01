resource "fm_ssh_key" "example" {
  name       = "my-ssh-key"
  public_key = file("~/.ssh/id_ed25519.pub")
}
