package virsh

import (
        "testing"
)


func TestListPools(t *testing.T) {
	vg:= "sbvg_datalake"
	UnMapAllDomains()
	UndefinePool(vg)	
	if IsPoolValid(vg) { 
		t.Errorf("Failed to Stop  %s ",vg)
	}
	if DefinePool(vg) != nil {
		t.Errorf("Failed to find %s to get started",vg)
	}
	if StartPool(vg) != nil {
		t.Errorf("Failed to Start pool %s ",vg)
	}

	ans := ListPools()
	t.Logf("POOLS: %v \n",ans)
	if len(ans) < 1 {   
		t.Errorf("Pools %s  ", ans)
	}
	for _, p := range ans {
		if ! IsPoolValid(p) { t.Errorf("Pool %s Should be valid ", p) }
	}
	if IsPoolValid("deadbeef") { t.Errorf("Bad Pool Should Not be valid ") }

	UndefinePool(vg)
	if  IsPoolValid(vg) { t.Errorf("Pool %s Should Not be valid ", vg) }

	DefinePool(vg) 
	if ! IsPoolValid(vg) { t.Errorf("Failed to define %s",vg) }

}

