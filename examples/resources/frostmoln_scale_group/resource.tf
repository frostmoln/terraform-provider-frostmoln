resource "frostmoln_scale_group" "web" {
  name                 = "web-servers"
  launch_template_id   = frostmoln_launch_template.web.id
  min_size             = 2
  max_size             = 10
  desired_capacity     = 3
  subnet_ids           = [frostmoln_subnet.a.id, frostmoln_subnet.b.id]
  load_balancer_pool_ids = [frostmoln_load_balancer_pool.web.id]

  health_check_type         = "elb"
  health_check_grace_period = 120
  cooldown_seconds          = 300
  termination_policy        = "oldest_first"

  tags = {
    environment = "production"
  }
}
