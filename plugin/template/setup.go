package template

import (
	"net"
	"regexp"
	"strings"
	gotmpl "text/template"

	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/pkg/upstream"

	"github.com/mholt/caddy"
	"github.com/miekg/dns"
	"github.com/yl2chen/cidranger"
)

func init() {
	caddy.RegisterPlugin("template", caddy.Plugin{
		ServerType: "dns",
		Action:     setupTemplate,
	})
}

func setupTemplate(c *caddy.Controller) error {
	handler, err := templateParse(c)
	if err != nil {
		return plugin.Error("template", err)
	}

	if err := setupMetrics(c); err != nil {
		return plugin.Error("template", err)
	}

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		handler.Next = next
		return handler
	})

	return nil
}

func templateParse(c *caddy.Controller) (handler Handler, err error) {
	handler.Templates = make([]template, 0)

	for c.Next() {
		if !c.NextArg() {
			return handler, c.ArgErr()
		}
		class, ok := dns.StringToClass[c.Val()]
		if !ok {
			return handler, c.Errf("invalid query class %s", c.Val())
		}

		if !c.NextArg() {
			return handler, c.ArgErr()
		}
		qtype, ok := dns.StringToType[c.Val()]
		if !ok {
			return handler, c.Errf("invalid RR class %s", c.Val())
		}

		zones := c.RemainingArgs()
		if len(zones) == 0 {
			zones = make([]string, len(c.ServerBlockKeys))
			copy(zones, c.ServerBlockKeys)
		}
		for i, str := range zones {
			zones[i] = plugin.Host(str).Normalize()
		}
		handler.Zones = append(handler.Zones, zones...)

		t := template{qclass: class, qtype: qtype, zones: zones}

		t.regex = make([]*regexp.Regexp, 0)
		templatePrefix := ""

		t.answer = make([]*gotmpl.Template, 0)

		var matchClients []*net.IPNet
		for c.NextBlock() {
			switch c.Val() {
			case "match":
				args := c.RemainingArgs()
				if len(args) == 0 {
					return handler, c.ArgErr()
				}
				for _, regex := range args {
					r, err := regexp.Compile(regex)
					if err != nil {
						return handler, c.Errf("could not parse regex: %s, %v", regex, err)
					}
					templatePrefix = templatePrefix + regex + " "
					t.regex = append(t.regex, r)
				}
			case "match-clients":
				args := c.RemainingArgs()
				if len(args) == 0 {
					return handler, c.ArgErr()
				}
				for _, cidr := range args {
					if strings.ToLower(cidr) == "any" {
						cidr = "0.0.0.0/0"
					}
					_, network, err := net.ParseCIDR(cidr)
					if err != nil {
						return handler, c.Errf("could not parse cidr: %s, %v", cidr, err)
					}
					matchClients = append(matchClients, network)
				}

			case "answer":
				args := c.RemainingArgs()
				if len(args) == 0 {
					return handler, c.ArgErr()
				}
				for _, answer := range args {
					tmpl, err := gotmpl.New("answer").Parse(answer)
					if err != nil {
						return handler, c.Errf("could not compile template: %s, %v", c.Val(), err)
					}
					t.answer = append(t.answer, tmpl)
				}

			case "additional":
				args := c.RemainingArgs()
				if len(args) == 0 {
					return handler, c.ArgErr()
				}
				for _, additional := range args {
					tmpl, err := gotmpl.New("additional").Parse(additional)
					if err != nil {
						return handler, c.Errf("could not compile template: %s, %v\n", c.Val(), err)
					}
					t.additional = append(t.additional, tmpl)
				}

			case "authority":
				args := c.RemainingArgs()
				if len(args) == 0 {
					return handler, c.ArgErr()
				}
				for _, authority := range args {
					tmpl, err := gotmpl.New("authority").Parse(authority)
					if err != nil {
						return handler, c.Errf("could not compile template: %s, %v\n", c.Val(), err)
					}
					t.authority = append(t.authority, tmpl)
				}

			case "rcode":
				if !c.NextArg() {
					return handler, c.ArgErr()
				}
				rcode, ok := dns.StringToRcode[c.Val()]
				if !ok {
					return handler, c.Errf("unknown rcode %s", c.Val())
				}
				t.rcode = rcode

			case "fallthrough":
				t.fall.SetZonesFromArgs(c.RemainingArgs())

			case "upstream":
				c.RemainingArgs() // eat remaining args
				t.upstream = upstream.New()
			default:
				return handler, c.ArgErr()
			}
		}

		if len(t.regex) == 0 {
			t.regex = append(t.regex, regexp.MustCompile(".*"))
		}
		if len(matchClients) == 0 {
			_, any, _ := net.ParseCIDR("0.0.0.0/0")
			t.ranger.Insert(cidranger.NewBasicRangerEntry(*any))
		} else {
			ranger := t.ranger
			for i := 0; i < len(matchClients); i++ {
				ranger.Insert(cidranger.NewBasicRangerEntry(*matchClients[i]))
			}
		}

		if len(t.answer) == 0 && len(t.authority) == 0 && t.rcode == dns.RcodeSuccess {
			return handler, c.Errf("no answer section for template found: %v", handler)
		}

		handler.Templates = append(handler.Templates, t)
	}

	return
}
